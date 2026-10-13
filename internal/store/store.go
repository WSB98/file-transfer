// Package store owns everything drop puts on disk. Its one promise is that
// it writes nowhere but the inbox, never overwrites a file, and never calls a
// file finished until every byte has been read back and checked.
//
// Files in progress live in a working folder inside the inbox, under names
// the store derives itself. Finished files are moved out into the inbox under
// a cleaned-up version of the sender's name.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WSB98/file-transfer/internal/human"
)

// WorkDir is the folder inside the inbox that holds files in progress. Clean
// names can't start with a dot, so no finished file can ever collide with it.
const WorkDir = ".incoming"

// MaxChunkSize bounds a chunk, and so the memory one in-flight chunk can hold.
const MaxChunkSize = 256 << 20

// maxUploads bounds the uploads one run keeps open. -max limits their bytes;
// this limits their number, and with it file handles and working files.
const maxUploads = 32

// Errors the caller can act on. Anything else is unexpected.
var (
	ErrInvalidSize   = errors.New("size must be positive")
	ErrTooLarge      = errors.New("over this run's size limit")
	ErrTooMany       = errors.New("too many uploads this run")
	ErrCannotResume  = errors.New("unfinished upload cannot be resumed")
	ErrNoSpace       = errors.New("not enough free disk space")
	ErrUnknownUpload = errors.New("unknown upload")
	ErrChunkInFlight = errors.New("chunk is already being written")
	ErrNotReceiving  = errors.New("upload is not accepting chunks")
	ErrMissingChunks = errors.New("upload is missing chunks")
	ErrHashMismatch  = errors.New("chunk does not match its hash")
	ErrInterrupted   = errors.New("chunk was cut off")
)

// Options configures a Store.
type Options struct {
	Inbox     string // an existing directory, already resolved (see ResolveInbox)
	ChunkSize int64
	MaxTotal  int64 // the most bytes this run accepts, across all files
	Reserve   int64 // free space to leave on the inbox's drive
	Logf      func(format string, args ...any)
}

// Store holds every upload of this run.
type Store struct {
	inbox, work string
	chunkSize   int64
	maxTotal    int64
	reserve     int64
	logf        func(format string, args ...any)
	freeSpace   func(dir string) (int64, error)
	closing     atomic.Bool

	mu       sync.Mutex
	uploads  map[string]*Upload
	order    []*Upload // uploads in the order they were opened this run
	allotted int64

	// diskMu lets one chunk write at a time. A hard drive writes fastest in
	// large in-order runs; parallel chunks interleaving their writes make the
	// head seek, which measured 191 MB/s down to 72 MB/s on a test HDD.
	diskMu sync.Mutex

	// finishMu serializes the end of uploads: picking a free name and taking
	// it must be one step, and its retries must not hold up mu.
	finishMu sync.Mutex
}

// New opens a store over an existing inbox, and checks that it can write
// there before anyone starts sending.
func New(o Options) (*Store, error) {
	s := &Store{
		inbox:     o.Inbox,
		work:      filepath.Join(o.Inbox, WorkDir),
		chunkSize: o.ChunkSize,
		maxTotal:  o.MaxTotal,
		reserve:   o.Reserve,
		logf:      o.Logf,
		freeSpace: diskFree,
		uploads:   make(map[string]*Upload),
	}
	if err := os.MkdirAll(s.work, 0o700); err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(s.work, "probe-*")
	if err != nil {
		return nil, fmt.Errorf("cannot write to the inbox: %w", err)
	}
	probe.Close()
	if err := os.Remove(probe.Name()); err != nil {
		return nil, fmt.Errorf("cannot clean up in the inbox: %w", err)
	}
	s.settle()
	return s, nil
}

// settle clears progress files whose upload already reached the inbox: drop
// stopped after the final move but before removing the progress file. Only a
// progress file that names its final file and has no data file left is
// touched, and only that progress file.
func (s *Store) settle() {
	for _, p := range s.progressFiles() {
		st, err := loadState(p)
		if err != nil || st.Final == "" {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(p), ".state.json")
		if _, err := os.Lstat(s.partPath(id)); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := os.Remove(p); err == nil {
			s.logf("%s was finished in an earlier run; cleared its leftover progress file", st.Final)
		}
	}
}

// progressFiles lists the progress files in the working folder. It reads the
// folder rather than globbing, since a glob pattern breaks on paths with [.
func (s *Store) progressFiles() []string {
	entries, _ := os.ReadDir(s.work)
	var paths []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".state.json") {
			paths = append(paths, filepath.Join(s.work, e.Name()))
		}
	}
	return paths
}

// Open returns the upload for this file: the one already open this run, one
// left unfinished by an earlier run, or a new one. isNew reports the last.
func (s *Store) Open(name string, size, modified int64) (u *Upload, isNew bool, err error) {
	if size <= 0 {
		return nil, false, ErrInvalidSize
	}
	id := uploadID(name, size, modified)

	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.uploads[id]; ok {
		return u, false, nil
	}
	if len(s.uploads) >= maxUploads {
		return nil, false, ErrTooMany
	}
	if size > s.maxTotal-s.allotted {
		return nil, false, ErrTooLarge
	}

	u, err = s.load(id, size)
	if errors.Is(err, fs.ErrNotExist) {
		u, err = s.create(id, sanitizeName(name), size, modified)
		isNew = true
	}
	if err != nil {
		return nil, false, err
	}
	s.uploads[id] = u
	s.order = append(s.order, u)
	s.allotted += size
	return u, isNew, nil
}

// Lookup finds an upload opened earlier this run.
// Uploads lists every upload opened this run, oldest first. Files from
// earlier runs appear only once someone picks them again.
func (s *Store) Uploads() []*Upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Upload(nil), s.order...)
}

func (s *Store) Lookup(id string) (*Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return nil, ErrUnknownUpload
	}
	return u, nil
}

// Active is the upload a progress display should follow: the most recently
// moving one, if anything moved in the last few seconds.
func (s *Store) Active(now time.Time) *Upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Upload
	for _, u := range s.uploads {
		t := u.touched.Load()
		if now.Sub(time.Unix(0, t)) < 5*time.Second && (best == nil || t > best.touched.Load()) {
			best = u
		}
	}
	return best
}

// Pending describes an unfinished upload from an earlier run.
type Pending struct {
	Name     string
	Size     int64
	Received int64
}

// Unfinished lists uploads left in the working folder by earlier runs, which
// resume when the sender picks the same file again.
func (s *Store) Unfinished() []Pending {
	var list []Pending
	for _, p := range s.progressFiles() {
		st, err := loadState(p)
		if err != nil {
			s.logf("unreadable progress file %s: %v", p, err)
			continue
		}
		list = append(list, Pending{Name: st.Name, Size: st.Size, Received: st.received()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// Close stops the store: an interrupted verify pass ends quietly, and every
// file is closed. Progress already made is on disk and resumes next run.
func (s *Store) Close() {
	s.closing.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.uploads {
		u.file.Close()
	}
}

func (s *Store) partPath(id string) string  { return filepath.Join(s.work, id+".part") }
func (s *Store) statePath(id string) string { return filepath.Join(s.work, id+".state.json") }

// load resumes an upload from an earlier run. A missing progress file returns
// an error matching fs.ErrNotExist. Anything else wrong is reported, not
// repaired: a damaged upload is never silently started over.
func (s *Store) load(id string, size int64) (*Upload, error) {
	st, err := loadState(s.statePath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil && st.Size != size {
		err = fmt.Errorf("it records %d bytes, not %d", st.Size, size)
	}
	if err != nil {
		return nil, s.damaged(id, fmt.Errorf("progress file: %v", err))
	}
	if st.ChunkSize > s.chunkSize {
		// A resumed upload keeps its chunk size, and the memory budget was
		// checked against this run's.
		return nil, fmt.Errorf("%w: it was started with %d MiB chunks; run drop with -chunk %d or more to resume it", ErrCannotResume, st.ChunkSize>>20, st.ChunkSize>>20)
	}
	f, err := os.OpenFile(s.partPath(id), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) && st.Final != "" {
		// The file reached the inbox; only the cleanup after it was missed.
		os.Remove(s.statePath(id))
		return s.finishedUpload(id, st), nil
	}
	if err != nil {
		return nil, s.damaged(id, err)
	}
	if fi, err := f.Stat(); err != nil || fi.Size() != st.Size {
		f.Close()
		return nil, s.damaged(id, fmt.Errorf("data file is not %d bytes", st.Size))
	}
	return s.newUpload(id, f, st), nil
}

// damaged explains how to recover from an upload drop can't resume, without
// deleting anything itself.
func (s *Store) damaged(id string, err error) error {
	return fmt.Errorf("%w (%v). To start it over, delete %s and %s", ErrCannotResume, err, s.partPath(id), s.statePath(id))
}

func (s *Store) create(id, name string, size, modified int64) (*Upload, error) {
	// Refuse uploads that would leave the drive nearly full: a full system
	// drive destabilizes Windows, and a full data drive surprises its owner.
	if free, err := s.freeSpace(s.work); err == nil && free-size < s.reserve {
		return nil, fmt.Errorf("%w: %s free, %s needed, and %s is kept free", ErrNoSpace, human.Bytes(free), human.Bytes(size), human.Bytes(s.reserve))
	}

	path := s.partPath(id)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := preallocate(f, size); err != nil {
		f.Close()
		os.Remove(path)
		return nil, fmt.Errorf("%w: %v", ErrNoSpace, err)
	}

	st := state{
		Name:      name,
		Size:      size,
		Modified:  modified,
		ChunkSize: s.chunkSize,
		Hashes:    make([]string, chunkCount(size, s.chunkSize)),
	}
	if err := saveState(s.statePath(id), st); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return s.newUpload(id, f, st), nil
}

// freeName returns name, or "stem (n).ext" for the first n that is unused.
// Nothing in the inbox is ever overwritten.
func freeName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 0; n < 10000; n++ {
		candidate := name
		if n > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		_, err := os.Lstat(filepath.Join(dir, candidate))
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no free name for %s", name)
}
