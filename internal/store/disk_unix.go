//go:build linux || darwin

package store

import (
	"os"
	"path/filepath"
	"syscall"
)

// diskFree reports the bytes an unprivileged user may still write to the
// filesystem holding dir.
func diskFree(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// preallocate sets the file's size. Unlike NTFS, most Unix filesystems make
// this a sparse file that reserves nothing, so the free-space check in create
// is what guards against filling the disk here.
func preallocate(f *os.File, size int64) error {
	return f.Truncate(size)
}

// moveNoReplace renames without ever replacing. rename(2) would overwrite
// dst; link(2) fails if dst exists, and the source is removed only after.
func moveNoReplace(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

func realPath(dir string) (string, error) {
	p, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}
