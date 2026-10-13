package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFinishVerifiesAndSavesUnderItsName(t *testing.T) {
	s, l := newTestStore(t)
	data := randomBytes(5*1024 + 7)
	u, _, _ := s.Open("holiday photos.zip", int64(len(data)), 1)
	putAll(t, u, data)

	if err := s.Finish(u); err != nil {
		t.Fatal(err)
	}
	st := waitFor(t, u, Done)
	want := sha256.Sum256(data)
	if st.SHA256 != hex.EncodeToString(want[:]) || st.Name != "holiday photos.zip" || st.Verified != int64(len(data)) {
		t.Errorf("status %+v", st)
	}
	if got, _ := os.ReadFile(filepath.Join(s.inbox, "holiday photos.zip")); !bytes.Equal(got, data) {
		t.Fatal("saved file differs")
	}
	if got := entries(t, s.inbox); !slices.Equal(got, []string{WorkDir, "holiday photos.zip"}) {
		t.Errorf("inbox holds %v", got)
	}
	if got := entries(t, s.work); len(got) != 0 {
		t.Errorf("working folder still holds %v", got)
	}
	if !l.contains("done: holiday photos.zip") {
		t.Error("completion was not logged")
	}
	if err := s.Finish(u); err != nil {
		t.Errorf("finishing again should be a no-op: %v", err)
	}
	if again, _, _ := s.Open("holiday photos.zip", int64(len(data)), 1); again.Status().Phase != Done {
		t.Error("re-picking a finished file should report it done")
	}
}

func TestFinishWithMissingChunksIsRefused(t *testing.T) {
	s, _ := newTestStore(t)
	data := randomBytes(3 * 1024)
	u, _, _ := s.Open("a.zip", 3072, 1)
	put(t, u, 0, chunkOf(data, 0, 1024))
	put(t, u, 2, chunkOf(data, 2, 1024))
	if err := s.Finish(u); !errors.Is(err, ErrMissingChunks) {
		t.Errorf("finish with chunk 1 missing: %v", err)
	}
	if u.Status().Phase != Receiving {
		t.Error("a refused finish changed the phase")
	}
}

func TestVerifyCatchesCorruptionOnDisk(t *testing.T) {
	s, l := newTestStore(t)
	data := randomBytes(4 * 1024)
	u, _, _ := s.Open("a.zip", 4096, 1)
	putAll(t, u, data)

	// Flip a byte in chunk 2 behind the store's back, as a bad disk would.
	f, _ := os.OpenFile(s.partPath(u.ID()), os.O_RDWR, 0)
	f.WriteAt([]byte{^data[2*1024+5]}, 2*1024+5)
	f.Close()

	s.Finish(u)
	waitFor(t, u, Receiving)
	if got := u.DoneChunks(); !slices.Equal(got, []int64{0, 1, 3}) {
		t.Fatalf("done after verify: %v, want chunk 2 reopened", got)
	}
	if !l.contains("did not match on disk") {
		t.Error("the bad chunk was not logged")
	}
	if st, _ := loadState(s.statePath(u.ID())); st.Hashes[2] != "" {
		t.Error("the reopened chunk is still marked done on disk")
	}

	put(t, u, 2, chunkOf(data, 2, 1024))
	s.Finish(u)
	waitFor(t, u, Done)
	if got, _ := os.ReadFile(filepath.Join(s.inbox, "a.zip")); !bytes.Equal(got, data) {
		t.Error("repaired file differs")
	}
}

func TestFinishNeverOverwrites(t *testing.T) {
	s, _ := newTestStore(t)
	existing := []byte("already here")
	os.WriteFile(filepath.Join(s.inbox, "a.zip"), existing, 0o600)
	os.WriteFile(filepath.Join(s.inbox, "a (1).zip"), existing, 0o600)

	data := randomBytes(1500)
	u, _, _ := s.Open("a.zip", 1500, 1)
	putAll(t, u, data)
	s.Finish(u)
	if st := waitFor(t, u, Done); st.Name != "a (2).zip" {
		t.Errorf("saved as %q, want a (2).zip", st.Name)
	}
	for _, n := range []string{"a.zip", "a (1).zip"} {
		if b, _ := os.ReadFile(filepath.Join(s.inbox, n)); !bytes.Equal(b, existing) {
			t.Errorf("%s was overwritten", n)
		}
	}
}

func TestTwoFilesWithTheSameNameBothSurvive(t *testing.T) {
	s, _ := newTestStore(t)
	a, b := randomBytes(1500), randomBytes(1700)
	ua, _, _ := s.Open("same.zip", 1500, 1)
	ub, _, _ := s.Open("same.zip", 1700, 2)
	putAll(t, ua, a)
	putAll(t, ub, b)
	s.Finish(ua)
	s.Finish(ub)
	names := []string{waitFor(t, ua, Done).Name, waitFor(t, ub, Done).Name}
	slices.Sort(names)
	if !slices.Equal(names, []string{"same (1).zip", "same.zip"}) {
		t.Fatalf("saved as %v", names)
	}
	for _, u := range []*Upload{ua, ub} {
		got, _ := os.ReadFile(filepath.Join(s.inbox, u.Status().Name))
		if want := map[*Upload][]byte{ua: a, ub: b}[u]; !bytes.Equal(got, want) {
			t.Errorf("%s holds the wrong bytes", u.Status().Name)
		}
	}
}

func TestMoveNoReplaceRefusesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	os.WriteFile(src, []byte("new"), 0o600)
	os.WriteFile(dst, []byte("old"), 0o600)
	if err := moveNoReplace(src, dst); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("moving onto an existing file: %v, want fs.ErrExist", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Fatal("the existing file was replaced")
	}
	os.Remove(dst)
	if err := moveNoReplace(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Error("the move lost the contents")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the source is still there after the move")
	}
}

func TestFinishWaitsOutAFileLock(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("open files only block renames on Windows")
	}
	old := renameWait
	renameWait = 20 * time.Millisecond
	t.Cleanup(func() { renameWait = old })

	s, _ := newTestStore(t)
	data := randomBytes(2048)
	u, _, _ := s.Open("a.zip", 2048, 1)
	putAll(t, u, data)

	// Hold the file open the way an antivirus scan would, which blocks the
	// move, then let go.
	lock, err := os.Open(s.partPath(u.ID()))
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(300*time.Millisecond, func() { lock.Close() })
	s.Finish(u)
	if st := waitFor(t, u, Done); st.Name != "a.zip" {
		t.Errorf("saved as %q", st.Name)
	}
}

func TestStoppingMidVerifyIsQuietAndResumes(t *testing.T) {
	inbox := t.TempDir()
	s, l := openTestStore(t, inbox, 1024)
	data := randomBytes(4096)
	u, _, _ := s.Open("a.zip", 4096, 1)
	putAll(t, u, data)

	// Stop the store halfway through the read-back, as Ctrl+C would.
	testHookVerifyChunk = func(i int64) {
		if i == 2 {
			s.Close()
		}
	}
	t.Cleanup(func() { testHookVerifyChunk = nil })
	s.Finish(u)
	for end := time.Now().Add(2 * time.Second); !l.contains("stopped verifying") && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	if !l.contains("stopped verifying") || l.contains("failed") {
		t.Fatalf("logs %q", l.lines)
	}
	if v := u.Status().Verified; v == 0 || v == 4096 {
		t.Errorf("verified %d bytes; the stop should land partway", v)
	}
	testHookVerifyChunk = nil

	s2, _ := openTestStore(t, inbox, 1024)
	r, _, err := s2.Open("a.zip", 4096, 1)
	if err != nil || len(r.DoneChunks()) != 4 {
		t.Fatalf("after restart: %v, done %v", err, r.DoneChunks())
	}
	s2.Finish(r)
	waitFor(t, r, Done)
}

func TestFinishInAVeryLongPath(t *testing.T) {
	// Past Windows' classic 260-character limit, which MoveFile enforces
	// unless given the long-path form.
	inbox := t.TempDir()
	for len(inbox) < 280 {
		inbox = filepath.Join(inbox, "a-rather-long-folder-name")
	}
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	s, _ := openTestStore(t, inbox, 1024)
	name := strings.Repeat("n", 150) + ".zip"
	data := randomBytes(1500)
	u, _, _ := s.Open(name, 1500, 1)
	putAll(t, u, data)
	s.Finish(u)
	waitFor(t, u, Done)
	if got, _ := os.ReadFile(filepath.Join(inbox, name)); !bytes.Equal(got, data) {
		t.Error("the file did not arrive intact at the long path")
	}
}
