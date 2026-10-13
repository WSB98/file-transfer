package store

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
)

func TestConcurrentChunksReassembleTheFile(t *testing.T) {
	s, _ := newTestStore(t)
	// The odd last byte makes the final chunk 1 byte long.
	data := randomBytes(7*1024 + 1)
	u, _, _ := s.Open("a.zip", int64(len(data)), 1)
	if u.Chunks() != 8 {
		t.Fatalf("%d chunks, want 8", u.Chunks())
	}
	if n, ok := u.ChunkLen(7); !ok || n != 1 {
		t.Fatalf("last chunk length %d", n)
	}
	if _, ok := u.ChunkLen(8); ok {
		t.Fatal("chunk 8 should not exist")
	}

	var wg sync.WaitGroup
	for w := int64(0); w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < u.Chunks(); i += 4 {
				if err := put(t, u, i, chunkOf(data, i, 1024)); err != nil {
					t.Errorf("chunk %d: %v", i, err)
				}
			}
		}()
	}
	wg.Wait()

	got, _ := os.ReadFile(s.partPath(u.ID()))
	if !bytes.Equal(got, data) {
		t.Fatal("bytes on disk differ from what was sent")
	}
	if st := u.Status(); st.Received != int64(len(data)) || len(u.DoneChunks()) != 8 {
		t.Errorf("status %+v", st)
	}
}

func TestABadChunkNeverReachesTheFile(t *testing.T) {
	s, _ := newTestStore(t)
	data := randomBytes(2048)
	u, _, _ := s.Open("a.zip", 2048, 1)

	if err := u.WriteChunk(0, bytes.NewReader(data[:1024]), make([]byte, 32)); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("wrong hash: %v", err)
	}
	if err := put(t, u, 1, data[1024:1500]); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("short body: %v", err)
	}
	if got, _ := os.ReadFile(s.partPath(u.ID())); !bytes.Equal(got, make([]byte, 2048)) {
		t.Error("rejected chunks wrote to the file")
	}
	if len(u.DoneChunks()) != 0 {
		t.Error("a rejected chunk was marked done")
	}
	if err := put(t, u, 0, data[:1024]); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := put(t, u, 0, data[:1024]); err != nil {
		t.Errorf("resending a done chunk should be a no-op: %v", err)
	}
	if err := put(t, u, 5, data[:10]); err == nil {
		t.Error("a chunk that doesn't exist was accepted")
	}
}

func TestChunkAlreadyInFlightOrNotReceiving(t *testing.T) {
	s, _ := newTestStore(t)
	data := randomBytes(2048)
	u, _, _ := s.Open("a.zip", 2048, 1)

	u.mu.Lock()
	u.inFlight[0] = true
	u.mu.Unlock()
	if err := put(t, u, 0, data[:1024]); !errors.Is(err, ErrChunkInFlight) {
		t.Errorf("same chunk twice at once: %v", err)
	}
	u.mu.Lock()
	delete(u.inFlight, 0)
	u.mu.Unlock()

	put(t, u, 1, data[1024:])
	u.setPhase(Verifying)
	if err := put(t, u, 0, data[:1024]); !errors.Is(err, ErrNotReceiving) {
		t.Errorf("chunk while verifying: %v", err)
	}
	if err := put(t, u, 1, data[1024:]); err != nil {
		t.Errorf("a done chunk stays a no-op while verifying: %v", err)
	}
}

func TestStatePersistsEveryVerifiedChunk(t *testing.T) {
	s, _ := newTestStore(t)
	data := randomBytes(3072)
	u, _, _ := s.Open("a.zip", 3072, 1)
	put(t, u, 1, chunkOf(data, 1, 1024))

	st, err := loadState(s.statePath(u.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if st.Hashes[0] != "" || st.Hashes[1] == "" || st.Hashes[2] != "" || st.received() != 1024 {
		t.Errorf("saved progress %+v", st)
	}
	if _, err := os.Stat(s.statePath(u.ID()) + ".tmp"); !os.IsNotExist(err) {
		t.Error("a temp progress file was left behind")
	}
	if got := u.DoneChunks(); !slices.Equal(got, []int64{1}) {
		t.Errorf("done %v", got)
	}
}
