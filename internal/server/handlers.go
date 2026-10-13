package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/WSB98/file-transfer/internal/human"
	"github.com/WSB98/file-transfer/internal/store"
)

const createBodyLimit = 4 << 10

var pageTypes = map[string]string{
	"index.html": "text/html; charset=utf-8",
	"app.js":     "text/javascript; charset=utf-8",
	"app.css":    "text/css; charset=utf-8",
	"icon.svg":   "image/svg+xml",
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name == "" {
		name = "index.html"
	}
	ctype, ok := pageTypes[name]
	if !ok {
		notFound(w)
		return
	}
	b, err := webFS.ReadFile("web/" + name)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Write(b)
}

type uploadInfo struct {
	ID        string  `json:"id"`
	State     string  `json:"state"`
	ChunkSize int64   `json:"chunkSize"`
	Chunks    int64   `json:"chunks"`
	Parallel  int     `json:"parallel"`
	Done      []int64 `json:"done"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Modified int64  `json:"modified"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, createBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.fail(w, errBadRequest)
		return
	}

	u, isNew, err := s.store.Open(req.Name, req.Size, req.Modified)
	if err != nil {
		if errors.Is(err, store.ErrNoSpace) || errors.Is(err, store.ErrTooLarge) || errors.Is(err, store.ErrTooMany) || errors.Is(err, store.ErrCannotResume) {
			// The sender's raw name never reaches the terminal: it could carry
			// control characters. Logged names are always cleaned ones.
			s.logf("refused a %s upload from %s: %v", human.Bytes(req.Size), peerIP(r), err)
		}
		s.fail(w, err)
		return
	}
	if name, ok := s.lobby.nameOf(r.Header.Get("X-Drop-Session")); ok {
		s.lobby.setSender(u.ID(), name)
	}
	st := u.Status()
	if u.NotePeer(peerIP(r)) {
		verb := "resuming"
		if isNew {
			verb = "new upload"
		}
		s.logf("%s: %s (%s, %.1f%% received) from %s", verb, st.Name, human.Bytes(st.Size), 100*float64(st.Received)/float64(st.Size), peerIP(r))
	}
	writeJSON(w, http.StatusOK, uploadInfo{
		ID:        u.ID(),
		State:     string(st.Phase),
		ChunkSize: u.ChunkSize(),
		Chunks:    u.Chunks(),
		Parallel:  s.parallel,
		Done:      u.DoneChunks(),
	})
}

type statusInfo struct {
	State     string  `json:"state"`
	Name      string  `json:"name"`
	Size      int64   `json:"size"`
	DoneBytes int64   `json:"doneBytes"`
	Verified  int64   `json:"verified"`
	Done      []int64 `json:"done"`
	SHA256    string  `json:"sha256,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.Lookup(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	st := u.Status()
	writeJSON(w, http.StatusOK, statusInfo{
		State:     string(st.Phase),
		Name:      st.Name,
		Size:      st.Size,
		DoneBytes: st.Received,
		Verified:  st.Verified,
		Done:      u.DoneChunks(),
		SHA256:    st.SHA256,
	})
}

func (s *Server) handleChunk(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.Lookup(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	i, err := strconv.ParseInt(r.PathValue("index"), 10, 64)
	n, ok := u.ChunkLen(i)
	if err != nil || !ok {
		s.fail(w, errBadIndex)
		return
	}
	if r.ContentLength != n {
		s.fail(w, errBadLength)
		return
	}
	want, err := hex.DecodeString(r.Header.Get("X-Chunk-SHA256"))
	if err != nil || len(want) != 32 {
		s.fail(w, errBadHash)
		return
	}

	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		s.fail(w, errBusy)
		return
	}

	if u.NotePeer(peerIP(r)) {
		s.logf("receiving %s from %s", u.Status().Name, peerIP(r))
	}
	body := &stallReader{
		r:     http.MaxBytesReader(w, r.Body, n),
		rc:    http.NewResponseController(w),
		idle:  s.chunkIdle,
		limit: time.Now().Add(s.chunkDeadline),
	}
	err = u.WriteChunk(i, body, want)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
		return
	case errors.Is(err, store.ErrInterrupted):
		s.logf("chunk %d of %s from %s was cut off; the browser will resend it", i, u.Status().Name, peerIP(r))
	case errors.Is(err, store.ErrHashMismatch):
		s.logf("chunk %d of %s from %s did not match its hash; the browser will resend it", i, u.Status().Name, peerIP(r))
	}
	s.fail(w, err)
}

func (s *Server) handleFinish(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.Lookup(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.Finish(u); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"state": string(u.Status().Phase)})
}

// stallReader gives a chunk upload two deadlines: the whole chunk within
// limit, and never longer than idle without a byte. When a connection dies
// silently, the stall frees its slot and its chunk in a minute instead of
// holding both until the overall limit, so the browser's retry gets through.
type stallReader struct {
	r     io.Reader
	rc    *http.ResponseController
	idle  time.Duration
	limit time.Time
}

func (s *stallReader) Read(p []byte) (int, error) {
	deadline := time.Now().Add(s.idle)
	if deadline.After(s.limit) {
		deadline = s.limit
	}
	// Only writers without deadlines (test recorders) return an error here.
	_ = s.rc.SetReadDeadline(deadline)
	return s.r.Read(p)
}
