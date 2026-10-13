package store

import (
	"encoding/json"
	"fmt"
	"os"
)

// state is an upload's progress file, which is what survives a restart.
// Hashes holds the verified SHA-256 of each chunk, or "" for one not yet
// received.
type state struct {
	Name      string   `json:"name"`
	Size      int64    `json:"size"`
	Modified  int64    `json:"modified"`
	ChunkSize int64    `json:"chunkSize"`
	Hashes    []string `json:"hashes"`

	// Set just before the final move, so a run that stops between the move
	// and removing this file can tell the upload finished.
	Final  string `json:"final,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

func chunkCount(size, chunkSize int64) int64 {
	return (size + chunkSize - 1) / chunkSize
}

func (st state) chunkLen(i int64) int64 {
	return min(st.ChunkSize, st.Size-i*st.ChunkSize)
}

func (st state) received() int64 {
	var n int64
	for i, h := range st.Hashes {
		if h != "" {
			n += st.chunkLen(int64(i))
		}
	}
	return n
}

func loadState(path string) (state, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return state{}, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return state{}, err
	}
	if st.Size <= 0 || st.ChunkSize <= 0 || st.ChunkSize > MaxChunkSize || int64(len(st.Hashes)) != chunkCount(st.Size, st.ChunkSize) {
		return state{}, fmt.Errorf("inconsistent sizes")
	}
	// The names end up in paths, so they must be exactly what drop itself
	// would have written.
	if st.Name == "" || sanitizeName(st.Name) != st.Name || (st.Final != "" && sanitizeName(st.Final) != st.Final) {
		return state{}, fmt.Errorf("unsafe file name")
	}
	for _, h := range st.Hashes {
		if h != "" && len(h) != 64 {
			return state{}, fmt.Errorf("malformed hash")
		}
	}
	return st, nil
}

// saveState replaces the progress file whole. The new contents are forced to
// disk before the rename, so a crash or power cut leaves either the old file
// or the new one, never a torn or empty one.
func saveState(path string, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
