package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/WSB98/file-transfer/internal/human"
)

// Finish starts the verify pass once every chunk is in. It returns at once;
// Status reports progress and the result. Calling it again while verifying,
// or after the upload is done or failed, changes nothing.
func (s *Store) Finish(u *Upload) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.phase != Receiving {
		return nil
	}
	if u.received != u.st.Size {
		return ErrMissingChunks
	}
	u.phase = Verifying
	go s.verify(u)
	return nil
}

var errStopped = errors.New("store closed")

// testHookVerifyChunk lets a test act partway through a verify pass.
var testHookVerifyChunk func(i int64)

func (s *Store) verify(u *Upload) {
	name, size := u.st.Name, u.st.Size
	s.logf("verifying %s (%s) by reading it back from disk", name, human.Bytes(size))
	sum, bad, err := u.readBack()
	switch {
	case s.closing.Load():
		s.logf("stopped verifying %s; it verifies again when the sender reconnects", name)
	case err != nil:
		u.setPhase(Failed)
		s.logf("verifying %s failed: %v. Its progress is saved; restart drop to try again.", name, err)
	case len(bad) > 0:
		u.reopen(bad)
		s.logf("%s: %d chunk(s) did not match on disk and will be resent", name, len(bad))
	default:
		final, err := s.complete(u, sum)
		if err != nil {
			u.setPhase(Failed)
			s.logf("saving %s failed: %v. Its progress is saved; restart drop to try again.", name, err)
			return
		}
		u.mu.Lock()
		u.phase, u.sha256, u.finalName = Done, sum, final
		u.mu.Unlock()
		s.logf("done: %s (%s)  SHA-256 %s", final, human.Bytes(size), sum)
	}
}

// readBack forces the file to disk, then re-reads it in order, checking every
// chunk against the hash it was accepted with and hashing the whole file in
// the same pass. This checks the bytes the OS will hand back later, not just
// the bytes that arrived. It returns the chunks that no longer match.
//
// No chunk can change during the pass: they are all done, and WriteChunk
// leaves done chunks alone.
func (u *Upload) readBack() (sum string, bad []int64, err error) {
	if err := u.file.Sync(); err != nil {
		return "", nil, err
	}
	u.verified.Store(0)
	whole := sha256.New()
	buf := make([]byte, 4<<20)
	for i := range u.Chunks() {
		if u.store.closing.Load() {
			return "", nil, errStopped
		}
		if testHookVerifyChunk != nil {
			testHookVerifyChunk(i)
		}
		h := sha256.New()
		section := io.NewSectionReader(u.file, i*u.chunkSize, u.st.chunkLen(i))
		if _, err := io.CopyBuffer(io.MultiWriter(h, whole, countingWriter{&u.moved}, countingWriter{&u.verified}), section, buf); err != nil {
			return "", nil, err
		}
		u.touch()
		if hex.EncodeToString(h.Sum(nil)) != u.st.Hashes[i] {
			bad = append(bad, i)
		}
	}
	return hex.EncodeToString(whole.Sum(nil)), bad, nil
}

func (u *Upload) reopen(bad []int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, i := range bad {
		u.st.Hashes[i] = ""
		u.received -= u.st.chunkLen(i)
	}
	u.phase = Receiving
	if err := saveState(u.statePath, u.st); err != nil {
		u.store.logf("saving progress for %s: %v", u.st.Name, err)
	}
}

func (u *Upload) setPhase(p Phase) {
	u.mu.Lock()
	u.phase = p
	u.mu.Unlock()
}

// renameWait is the first pause between rename attempts; it grows each time.
// Antivirus and indexers briefly lock a file they are scanning, and a large
// new file is exactly what they scan.
var renameWait = time.Second

// complete moves the verified file out of the working folder under a free
// name, then removes its progress file. The move itself refuses to replace
// anything, so even a file that appears under the chosen name at the last
// instant is safe: the move fails, and the next free name is tried.
func (s *Store) complete(u *Upload, sum string) (string, error) {
	if err := u.file.Close(); err != nil {
		return "", err
	}
	s.finishMu.Lock()
	defer s.finishMu.Unlock()
	src := s.partPath(u.id)
	var name string
	var err error
	for waits, tries := 0, 0; tries < 100; tries++ {
		name, err = freeName(s.inbox, u.st.Name)
		if err != nil {
			return "", err
		}
		// Record the destination first. If drop stops right after the move,
		// the next run sees the file arrived instead of a damaged upload.
		u.mu.Lock()
		u.st.Final, u.st.SHA256 = name, sum
		err = saveState(u.statePath, u.st)
		u.mu.Unlock()
		if err != nil {
			return "", err
		}
		err = moveNoReplace(src, filepath.Join(s.inbox, name))
		if err == nil {
			break
		}
		if errors.Is(err, fs.ErrExist) {
			continue // the name was taken after it was picked; pick the next
		}
		if waits++; waits == 9 {
			break
		}
		time.Sleep(time.Duration(waits) * renameWait)
	}
	if err != nil {
		return "", err
	}
	if err := os.Remove(u.statePath); err != nil {
		s.logf("removing the progress file for %s: %v", name, err)
	}
	return name, nil
}
