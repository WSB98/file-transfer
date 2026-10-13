package store

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx        = kernel32.NewProc("GetDiskFreeSpaceExW")
	getFinalPathNameByHandleW = kernel32.NewProc("GetFinalPathNameByHandleW")
)

// diskFree reports the bytes this user may still write to the volume holding
// dir, honoring disk quotas.
func diskFree(dir string) (int64, error) {
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free uint64
	ok, _, err := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0)
	if ok == 0 {
		return 0, err
	}
	return int64(free), nil
}

// preallocate sets the file's size, which on NTFS reserves the space: it
// fails at once when the disk is too small, and later writes can't run out.
func preallocate(f *os.File, size int64) error {
	return f.Truncate(size)
}

// moveNoReplace renames without ever replacing: MoveFile fails if dst exists,
// where os.Rename on Windows would silently overwrite it.
func moveNoReplace(src, dst string) error {
	from, err := syscall.UTF16PtrFromString(longPath(src))
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(longPath(dst))
	if err != nil {
		return err
	}
	if err := syscall.MoveFile(from, to); err != nil {
		return &os.LinkError{Op: "move", Old: src, New: dst, Err: err}
	}
	return nil
}

// longPath gives an absolute path Windows' long-path form, which lifts the
// 260-character limit. The os package does this itself; raw syscalls don't.
func longPath(p string) string {
	switch {
	case !filepath.IsAbs(p), strings.HasPrefix(p, `\\?\`):
		return p
	case strings.HasPrefix(p, `\\`):
		return `\\?\UNC\` + p[2:]
	default:
		return `\\?\` + filepath.Clean(p)
	}
}

// realPath asks Windows where a directory really lives, through any chain of
// junctions or symlinks. Since Go 1.23, filepath.EvalSymlinks no longer
// follows junctions, and an inbox on another drive is usually one.
func realPath(dir string) (string, error) {
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	// FILE_FLAG_BACKUP_SEMANTICS is what lets CreateFile open a directory.
	h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)

	buf := make([]uint16, syscall.MAX_LONG_PATH)
	n, _, err := getFinalPathNameByHandleW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if n == 0 || int(n) > len(buf) {
		return "", err
	}
	// Windows answers in its long-path form (\\?\D:\x, or \\?\UNC\server\x).
	path := syscall.UTF16ToString(buf[:n])
	if rest, ok := strings.CutPrefix(path, `\\?\UNC\`); ok {
		return `\\` + rest, nil
	}
	return strings.TrimPrefix(path, `\\?\`), nil
}
