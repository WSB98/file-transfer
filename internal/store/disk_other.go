//go:build !windows && !linux && !darwin

package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Untested platforms get the portable fallbacks: no free-space check, a
// possibly sparse preallocation, and a check-then-rename move.

func diskFree(dir string) (int64, error) {
	return 0, errors.New("free space is not available on this OS")
}

func preallocate(f *os.File, size int64) error {
	return f.Truncate(size)
}

func moveNoReplace(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return &os.LinkError{Op: "move", Old: src, New: dst, Err: fs.ErrExist}
	}
	return os.Rename(src, dst)
}

func realPath(dir string) (string, error) {
	p, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}
