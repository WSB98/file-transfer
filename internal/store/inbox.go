package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ResolveInbox follows the inbox path through any junction or symlink to the
// real directory, and refuses a missing one. Creating it silently is how a
// 200 GB upload would land on the wrong drive when a junction goes missing.
func ResolveInbox(path string) (string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inbox %q does not exist. Create it, or link it to a roomy drive (see the README)", path)
	}
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("inbox %q is not a directory", path)
	}
	return realPath(path)
}

// DiskFree reports the free space on the drive holding dir, where the
// platform can tell.
func DiskFree(dir string) (int64, error) {
	return diskFree(dir)
}
