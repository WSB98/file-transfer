package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Phase is where an upload is in its life: receiving, then verifying, then
// done. A verify pass that finds bad chunks sends it back to receiving; one
// that hits an I/O error parks it in failed until the next run.
type Phase string

const (
	Receiving Phase = "receiving"
	Verifying Phase = "verifying"
	Done      Phase = "done"
	Failed    Phase = "failed"
)

// Upload is one file being received.
type Upload struct {
	id        string
	file      *os.File
	statePath string
	store     *Store
	chunkSize int64

	// moved counts bytes streamed in or read back, verified counts bytes read
	// back by the current verify pass, and touched is the last time either
	// moved. They feed progress displays without taking the lock.
	moved    atomic.Int64
	verified atomic.Int64
	touched  atomic.Int64

	mu        sync.Mutex
	st        state
	inFlight  map[int64]bool
	received  int64
	phase     Phase
	peer      string
	sha256    string
	finalName string
}

// uploadID is derived from what the sender's browser knows about the file, so
// picking the same file again after a reload or restart lands on the same
// upload.
func uploadID(name string, size, modified int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%s", size, modified, name)))
	return hex.EncodeToString(sum[:16])
}

func (s *Store) newUpload(id string, f *os.File, st state) *Upload {
	return &Upload{
		id:        id,
		file:      f,
		statePath: s.statePath(id),
		store:     s,
		chunkSize: st.ChunkSize,
		st:        st,
		inFlight:  make(map[int64]bool),
		received:  st.received(),
		phase:     Receiving,
	}
}

// finishedUpload stands for an upload that reached the inbox in an earlier
// run, so picking the same file again reports it done.
func (s *Store) finishedUpload(id string, st state) *Upload {
	u := s.newUpload(id, nil, st)
	u.phase, u.finalName, u.sha256 = Done, st.Final, st.SHA256
	return u
}

func (u *Upload) ID() string              { return u.id }
func (u *Upload) ChunkSize() int64        { return u.chunkSize }
func (u *Upload) Chunks() int64           { return int64(len(u.st.Hashes)) }
func (u *Upload) Moved() int64            { return u.moved.Load() }
func (u *Upload) touch()                  { u.touched.Store(time.Now().UnixNano()) }
func (u *Upload) validIndex(i int64) bool { return 0 <= i && i < u.Chunks() }

// ChunkLen is the exact length of chunk i, and false if there is no chunk i.
func (u *Upload) ChunkLen(i int64) (int64, bool) {
	if !u.validIndex(i) {
		return 0, false
	}
	return u.st.chunkLen(i), true
}

// Status is a consistent snapshot of an upload.
type Status struct {
	Phase    Phase
	Name     string // the sender's name, cleaned; once done, the name it was saved under
	Size     int64
	Received int64 // bytes in verified chunks
	Verified int64 // bytes read back by the current verify pass
	SHA256   string
}

func (u *Upload) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := Status{
		Phase:    u.phase,
		Name:     u.st.Name,
		Size:     u.st.Size,
		Received: u.received,
		Verified: u.verified.Load(),
		SHA256:   u.sha256,
	}
	if u.finalName != "" {
		s.Name = u.finalName
	}
	return s
}

// DoneChunks lists the chunks received and verified so far.
func (u *Upload) DoneChunks() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	done := []int64{}
	for i, h := range u.st.Hashes {
		if h != "" {
			done = append(done, int64(i))
		}
	}
	return done
}

// NotePeer records who is sending and reports whether that changed, so a new
// address can be logged once rather than per chunk.
func (u *Upload) NotePeer(ip string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.peer == ip {
		return false
	}
	u.peer = ip
	return true
}

// countingWriter feeds progress as bytes stream, so a rate reads smoothly
// instead of jumping once per chunk.
type countingWriter struct{ n *atomic.Int64 }

func (c countingWriter) Write(p []byte) (int, error) {
	c.n.Add(int64(len(p)))
	return len(p), nil
}

var chunkBufs sync.Pool

// chunkBuf returns a pooled buffer of length n, so steady uploading reuses a
// few chunk-sized buffers instead of allocating one per request.
func chunkBuf(n int64) *[]byte {
	if b, ok := chunkBufs.Get().(*[]byte); ok && int64(cap(*b)) >= n {
		*b = (*b)[:n]
		return b
	}
	b := make([]byte, n)
	return &b
}

// WriteChunk receives chunk i from body (exactly ChunkLen(i) bytes) and stores
// it if its SHA-256 equals want. The whole chunk is read into memory and
// checked first, so bad or partial data never reaches the file, and the disk
// sees one large write per chunk. Sending a chunk that is already done is a
// harmless no-op.
func (u *Upload) WriteChunk(i int64, body io.Reader, want []byte) error {
	n, ok := u.ChunkLen(i)
	if !ok {
		return fmt.Errorf("no chunk %d", i)
	}
	u.mu.Lock()
	switch {
	case u.st.Hashes[i] != "":
		u.mu.Unlock()
		return nil
	case u.phase != Receiving:
		u.mu.Unlock()
		return ErrNotReceiving
	case u.inFlight[i]:
		u.mu.Unlock()
		return ErrChunkInFlight
	}
	u.inFlight[i] = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		delete(u.inFlight, i)
		u.mu.Unlock()
	}()

	u.touch()
	buf := chunkBuf(n)
	defer chunkBufs.Put(buf)
	_, err := io.ReadFull(io.TeeReader(body, countingWriter{&u.moved}), *buf)
	u.touch()
	if err != nil {
		// The connection stalled or dropped mid-chunk. That's the network, not
		// a fault here: nothing was written, and the sender resends.
		return fmt.Errorf("%w: %v", ErrInterrupted, err)
	}
	sum := sha256.Sum256(*buf)
	if !bytes.Equal(sum[:], want) {
		return ErrHashMismatch
	}

	u.store.diskMu.Lock()
	_, err = u.file.WriteAt(*buf, i*u.chunkSize)
	u.store.diskMu.Unlock()
	if err != nil {
		return fmt.Errorf("writing chunk %d: %w", i, err)
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	u.st.Hashes[i] = hex.EncodeToString(sum[:])
	u.received += n
	// A failed save only costs a resend after a restart: the in-memory state
	// is still right, and the next successful save includes this chunk.
	if err := saveState(u.statePath, u.st); err != nil {
		u.store.logf("saving progress for %s: %v", u.st.Name, err)
	}
	return nil
}
