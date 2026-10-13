package store

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestStore opens a store over a fresh temp inbox with 1 KiB chunks and
// plenty of (pretend) free space. Logs are kept for assertions.
func newTestStore(t *testing.T) (*Store, *logs) {
	t.Helper()
	return openTestStore(t, t.TempDir(), 1024)
}

func openTestStore(t *testing.T, inbox string, chunkSize int64) (*Store, *logs) {
	t.Helper()
	l := &logs{t: t}
	s, err := New(Options{Inbox: inbox, ChunkSize: chunkSize, MaxTotal: 1 << 30, Reserve: 0, Logf: l.logf})
	if err != nil {
		t.Fatal(err)
	}
	s.freeSpace = func(string) (int64, error) { return 1 << 40, nil }
	t.Cleanup(s.Close)
	return s, l
}

type logs struct {
	t     *testing.T
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf(format, args...)
	l.lines = append(l.lines, line)
	l.t.Log(line)
}

func (l *logs) contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func chunkOf(data []byte, i, size int64) []byte {
	return data[i*size : min(int64(len(data)), (i+1)*size)]
}

func put(t *testing.T, u *Upload, i int64, data []byte) error {
	t.Helper()
	sum := sha256.Sum256(data)
	return u.WriteChunk(i, bytes.NewReader(data), sum[:])
}

func putAll(t *testing.T, u *Upload, data []byte) {
	t.Helper()
	for i := range u.Chunks() {
		if err := put(t, u, i, chunkOf(data, i, u.ChunkSize())); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
}

// waitFor polls until the upload leaves the verifying phase.
func waitFor(t *testing.T, u *Upload, want Phase) Status {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if st := u.Status(); st.Phase != Verifying {
			if st.Phase != want {
				t.Fatalf("phase %q, want %q", st.Phase, want)
			}
			return st
		}
	}
	t.Fatal("still verifying after 5s")
	return Status{}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, _ := os.ReadDir(dir)
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func TestNewPreparesAWritableWorkingFolder(t *testing.T) {
	inbox := t.TempDir()
	openTestStore(t, inbox, 1024)
	if got := entries(t, inbox); !slices.Equal(got, []string{WorkDir}) {
		t.Errorf("inbox holds %v, want only %s", got, WorkDir)
	}
	if got := entries(t, filepath.Join(inbox, WorkDir)); len(got) != 0 {
		t.Errorf("the write probe was left behind: %v", got)
	}

	file := filepath.Join(t.TempDir(), "not-a-dir")
	os.WriteFile(file, nil, 0o600)
	if _, err := New(Options{Inbox: file, ChunkSize: 1024, MaxTotal: 1, Logf: t.Logf}); err == nil {
		t.Error("a file was accepted as the inbox")
	}
}

func TestReopeningResumesInMemoryAndAcrossRestarts(t *testing.T) {
	inbox := t.TempDir()
	s, _ := openTestStore(t, inbox, 1024)
	data := randomBytes(3000)
	u, isNew, err := s.Open("a.zip", 3000, 1)
	if err != nil || !isNew {
		t.Fatalf("open: %v, new %v", err, isNew)
	}
	put(t, u, 0, chunkOf(data, 0, 1024))
	put(t, u, 2, chunkOf(data, 2, 1024))

	again, isNew, _ := s.Open("a.zip", 3000, 1)
	if again != u || isNew {
		t.Error("reopening in the same run must return the same upload")
	}
	if other, _, _ := s.Open("a.zip", 3000, 2); other.ID() == u.ID() {
		t.Error("a different modified time must be a different upload")
	}

	// Restart with bigger chunks: the saved upload keeps its own.
	s.Close()
	s2, _ := openTestStore(t, inbox, 2048)
	r, isNew, err := s2.Open("a.zip", 3000, 1)
	if err != nil || isNew || r.ID() != u.ID() || r.ChunkSize() != 1024 || !slices.Equal(r.DoneChunks(), []int64{0, 2}) {
		t.Fatalf("after restart: %v, new %v, chunk size %d, done %v", err, isNew, r.ChunkSize(), r.DoneChunks())
	}
	if p := s2.Unfinished(); len(p) != 2 || p[0].Name != "a.zip" {
		t.Errorf("unfinished uploads: %+v", p)
	}
	put(t, r, 1, chunkOf(data, 1, 1024))
	s2.Finish(r)
	waitFor(t, r, Done)
	if got, _ := os.ReadFile(filepath.Join(inbox, "a.zip")); !bytes.Equal(got, data) {
		t.Error("resumed file differs from what was sent")
	}
}

func TestDamagedProgressIsReportedNeverOverwritten(t *testing.T) {
	inbox := t.TempDir()
	s, _ := openTestStore(t, inbox, 1024)
	u, _, _ := s.Open("a.zip", 3000, 1)
	statePath := s.statePath(u.ID())
	s.Close()

	for name, contents := range map[string]string{
		"not json":         "{not json",
		"wrong chunk list": `{"name":"a.zip","size":3000,"modified":1,"chunkSize":1024,"hashes":[""]}`,
		"malformed hash":   `{"name":"a.zip","size":3000,"modified":1,"chunkSize":1024,"hashes":["ab","",""]}`,
	} {
		os.WriteFile(statePath, []byte(contents), 0o600)
		s2, _ := openTestStore(t, inbox, 1024)
		_, _, err := s2.Open("a.zip", 3000, 1)
		if err == nil || !strings.Contains(err.Error(), "delete") || !strings.Contains(err.Error(), statePath) {
			t.Errorf("%s: got %v, want a message saying which files to delete", name, err)
		}
		if b, _ := os.ReadFile(statePath); string(b) != contents {
			t.Errorf("%s: the damaged progress file was changed", name)
		}
		s2.Close()
	}
}

func TestLeftoverTempProgressFileIsHarmless(t *testing.T) {
	inbox := t.TempDir()
	s, _ := openTestStore(t, inbox, 1024)
	data := randomBytes(2048)
	u, _, _ := s.Open("a.zip", 2048, 1)
	put(t, u, 0, chunkOf(data, 0, 1024))
	s.Close()

	// A crash mid-save leaves a half-written temp file beside the real one.
	os.WriteFile(s.statePath(u.ID())+".tmp", []byte("{half"), 0o600)
	s2, _ := openTestStore(t, inbox, 1024)
	r, _, err := s2.Open("a.zip", 2048, 1)
	if err != nil || !slices.Equal(r.DoneChunks(), []int64{0}) {
		t.Fatalf("resume past a torn temp file: %v %v", err, r.DoneChunks())
	}
	if err := put(t, r, 1, chunkOf(data, 1, 1024)); err != nil {
		t.Fatal(err)
	}
}

func TestRunCapCountsEveryFileOnce(t *testing.T) {
	s, _ := newTestStore(t)
	s.maxTotal = 10_000
	if _, _, err := s.Open("a", 6000, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open("a", 6000, 1); err != nil {
		t.Fatalf("reopening the same file must not count twice: %v", err)
	}
	if _, _, err := s.Open("b", 6000, 1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("second file over the cap: %v", err)
	}
	if _, _, err := s.Open("c", 4000, 1); err != nil {
		t.Fatalf("second file within the cap: %v", err)
	}
	if _, _, err := s.Open("d", 0, 1); !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("empty file: %v", err)
	}
}

func TestKeepsFreeSpaceInReserve(t *testing.T) {
	s, _ := newTestStore(t)
	s.freeSpace = func(string) (int64, error) { return 100_000, nil }
	s.reserve = 50_000

	_, _, err := s.Open("big", 60_000, 1)
	if !errors.Is(err, ErrNoSpace) || !strings.Contains(err.Error(), "kept free") {
		t.Fatalf("an upload eating into the reserve: %v", err)
	}
	if got := entries(t, s.work); len(got) != 0 {
		t.Errorf("a refused upload left %v behind", got)
	}
	if _, _, err := s.Open("small", 40_000, 1); err != nil {
		t.Fatalf("an upload that leaves the reserve: %v", err)
	}
}

func TestDiskReservationFailureIsClean(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only NTFS reserves space on truncate; elsewhere the free-space check guards this")
	}
	s, _ := newTestStore(t)
	free, err := diskFree(s.work)
	if err != nil {
		t.Fatal(err)
	}
	// One GiB more than the drive has, with the free-space check stubbed out,
	// so it is the reservation itself that must refuse.
	s.maxTotal = 1 << 62
	if _, _, err := s.Open("huge", free+1<<30, 1); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("%d bytes on a drive with %d free: %v", free+1<<30, free, err)
	}
	if got := entries(t, s.work); len(got) != 0 {
		t.Errorf("a failed reservation left %v behind", got)
	}
}

func TestResumeWithSmallerChunksIsRefusedWithAFix(t *testing.T) {
	inbox := t.TempDir()
	s, _ := openTestStore(t, inbox, 2048)
	u, _, _ := s.Open("a.zip", 5000, 1)
	put(t, u, 0, randomBytes(2048))
	s.Close()

	// This run's memory budget assumes 1 KiB chunks; the upload needs 2 KiB.
	s2, _ := openTestStore(t, inbox, 1024)
	_, _, err := s2.Open("a.zip", 5000, 1)
	if !errors.Is(err, ErrCannotResume) || !strings.Contains(err.Error(), "-chunk") {
		t.Fatalf("got %v, want ErrCannotResume naming -chunk", err)
	}
	if st, _ := loadState(s.statePath(u.ID())); st.received() != 2048 {
		t.Error("the refused upload's progress was changed")
	}
}

func TestTooManyUploadsInOneRun(t *testing.T) {
	s, _ := newTestStore(t)
	for i := range maxUploads {
		if _, _, err := s.Open(fmt.Sprintf("f%d", i), 10, 1); err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
	}
	if _, _, err := s.Open("one more", 10, 1); !errors.Is(err, ErrTooMany) {
		t.Fatalf("upload %d: %v, want ErrTooMany", maxUploads+1, err)
	}
	if _, _, err := s.Open("f0", 10, 1); err != nil {
		t.Errorf("reopening an existing upload must still work: %v", err)
	}
}

func TestUnsafeNamesInAProgressFileAreRefused(t *testing.T) {
	inbox := t.TempDir()
	s, _ := openTestStore(t, inbox, 1024)
	u, _, _ := s.Open("a.zip", 1000, 1)
	statePath := s.statePath(u.ID())
	s.Close()

	for _, c := range []struct{ state, reason string }{
		{`{"name":"../escape.zip","size":1000,"modified":1,"chunkSize":1024,"hashes":[""]}`, "unsafe file name"},
		{`{"name":"a.zip","size":1000,"modified":1,"chunkSize":1024,"hashes":[""],"final":"../up.zip"}`, "unsafe file name"},
		{`{"name":"a.zip","size":1000,"modified":1,"chunkSize":1024,"hashes":[""],"final":"..\\up.zip"}`, "unsafe file name"},
		{`{"name":"a.zip","size":1000,"modified":1,"chunkSize":1073741824,"hashes":[""]}`, "inconsistent sizes"},
	} {
		os.WriteFile(statePath, []byte(c.state), 0o600)
		s2, _ := openTestStore(t, inbox, 1024)
		_, _, err := s2.Open("a.zip", 1000, 1)
		if !errors.Is(err, ErrCannotResume) || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s: got %v, want ErrCannotResume for %q", c.state, err, c.reason)
		}
		s2.Close()
	}
}

func TestStoppedAfterTheMoveCountsAsFinished(t *testing.T) {
	inbox := t.TempDir()
	s, _ := openTestStore(t, inbox, 1024)
	data := randomBytes(2500)
	u, _, _ := s.Open("a.zip", 2500, 1)
	putAll(t, u, data)
	s.Finish(u)
	want := waitFor(t, u, Done)

	// Recreate the moment between the move and removing the progress file.
	st := u.st
	st.Final, st.SHA256 = want.Name, want.SHA256
	if err := saveState(s.statePath(u.ID()), st); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, l := openTestStore(t, inbox, 1024)
	if !l.contains("finished in an earlier run") {
		t.Error("the leftover progress file was not settled")
	}
	if p := s2.Unfinished(); len(p) != 0 {
		t.Errorf("a finished upload is listed as unfinished: %+v", p)
	}
	if got := entries(t, s2.work); len(got) != 0 {
		t.Errorf("working folder still holds %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(inbox, "a.zip")); !bytes.Equal(b, data) {
		t.Error("the finished file was disturbed")
	}

	// The same moment, but the sender reconnects before any restart settles it.
	saveState(s.statePath(u.ID()), st)
	r, _, err := s2.Open("a.zip", 2500, 1)
	if err != nil || r.Status().Phase != Done || r.Status().Name != "a.zip" || r.Status().SHA256 != want.SHA256 {
		t.Fatalf("reopen: %v %+v", err, r.Status())
	}
}

func TestUnfinishedSurvivesBracketsInThePath(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "my [files]")
	os.Mkdir(inbox, 0o700)
	s, _ := openTestStore(t, inbox, 1024)
	u, _, _ := s.Open("a.zip", 3000, 1)
	put(t, u, 0, randomBytes(1024))
	if p := s.Unfinished(); len(p) != 1 || p[0].Received != 1024 {
		t.Errorf("unfinished uploads under a [bracketed] path: %+v", p)
	}
}

func TestUploadsListsThisRunInOrder(t *testing.T) {
	s, _ := newTestStore(t)
	a, _, _ := s.Open("a.zip", 100, 1)
	b, _, _ := s.Open("b.zip", 100, 1)
	s.Open("a.zip", 100, 1) // reopening doesn't add it twice
	got := s.Uploads()
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("uploads %v", got)
	}
}
