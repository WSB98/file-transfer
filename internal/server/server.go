// Package server is drop's HTTPS side: the page the sender opens, and the
// small API it uploads through. Every request must carry the link's token
// before it is even routed, and every API call also needs the key.
package server

import (
	"crypto/tls"
	"embed"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/WSB98/file-transfer/internal/creds"
	"github.com/WSB98/file-transfer/internal/store"
)

//go:embed web
var webFS embed.FS

const (
	requestReadTimeout = 30 * time.Second
	logInterval        = 30 * time.Second
)

// Config is what a Server needs.
type Config struct {
	Token    string // the secret in the link
	Key      string // the second factor the sender types
	Store    *store.Store
	Parallel int // chunks the browser sends at once
	Logf     func(format string, args ...any)
}

type Server struct {
	token, key    string
	store         *store.Store
	parallel      int
	logf          func(format string, args ...any)
	slots         chan struct{}
	chunkDeadline time.Duration
	chunkIdle     time.Duration

	lobby    *lobby
	rejected throttle
	badKeys  throttle
}

func New(c Config) *Server {
	return &Server{
		token:    c.Token,
		key:      c.Key,
		store:    c.Store,
		parallel: c.Parallel,
		logf:     c.Logf,
		// Room for every browser worker plus its retries, and no more.
		slots:         make(chan struct{}, c.Parallel*2),
		chunkDeadline: 10 * time.Minute,
		chunkIdle:     time.Minute,
		lobby:         newLobby(),
	}
}

// HTTPServer wraps a handler in the timeouts and TLS settings a server on the
// open internet needs.
func HTTPServer(h http.Handler, cert tls.Certificate) *http.Server {
	return &http.Server{
		Handler: h,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		},
		// A non-nil, empty map turns HTTP/2 off. Over HTTP/2 the browser would
		// squeeze every parallel chunk into one TCP connection, and Go's
		// default per-connection upload window would cap the transfer at a
		// fraction of the link.
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// Open ports draw TLS scanners, and each failed handshake would print a
		// line. The handler logs what matters.
		ErrorLog: log.New(io.Discard, "", 0),
	}
}

// Handler is the front door. An outsider can't tell one path from another:
// without the token, everything is an empty 404.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /t/{token}/{file...}", s.handlePage)
	mux.HandleFunc("POST /api/join", s.handleJoin)
	mux.HandleFunc("GET /api/lobby", s.handleLobby)
	mux.HandleFunc("POST /api/uploads", s.handleCreate)
	mux.HandleFunc("GET /api/uploads/{id}", s.handleStatus)
	mux.HandleFunc("PUT /api/uploads/{id}/chunks/{index}", s.handleChunk)
	mux.HandleFunc("POST /api/uploads/{id}/finish", s.handleFinish)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer s.recoverPanic(w)
		setSecurityHeaders(w.Header())
		// Bounds how long any request may take to send its body. Chunk uploads
		// set their own. The error only fires on writers without deadlines,
		// like test recorders.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(requestReadTimeout))

		if !s.authorized(r) {
			if n, ok := s.rejected.note(); ok {
				s.logf("rejected %d request(s) without the link, latest from %s", n, peerIP(r))
			}
			notFound(w)
			return
		}
		// Unknown routes and wrong methods look the same as everything else.
		if _, pattern := mux.Handler(r); pattern == "" {
			notFound(w)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !creds.KeyMatches(r.Header.Get("X-Drop-Key"), s.key) {
			// Only someone holding the link gets this far, so a wrong key is
			// worth a loud line even when it is just a typo.
			if n, ok := s.badKeys.note(); ok {
				s.logf("WARNING: %d request(s) with the link but a wrong key, latest from %s. If that isn't your friend, press Ctrl+C.", n, peerIP(r))
			}
			s.fail(w, errBadKey)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if rest, ok := strings.CutPrefix(r.URL.Path, "/t/"); ok {
		token, _, _ := strings.Cut(rest, "/")
		return creds.TokenMatches(token, s.token)
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return ok && creds.TokenMatches(token, s.token)
	}
	return false
}

func (s *Server) recoverPanic(w http.ResponseWriter) {
	v := recover()
	if v == nil {
		return
	}
	if v == http.ErrAbortHandler {
		panic(v)
	}
	s.logf("panic: %v", v)
	w.WriteHeader(http.StatusInternalServerError)
}

// throttle collapses a repeated event into one log line per interval. The
// first event always logs, so the start of trouble is never hidden.
type throttle struct {
	mu    sync.Mutex
	count int
	last  time.Time
}

func (t *throttle) note() (count int, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	if time.Since(t.last) < logInterval {
		return 0, false
	}
	count, t.count, t.last = t.count, 0, time.Now()
	return count, true
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
}
