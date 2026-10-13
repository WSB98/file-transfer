package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveInboxRefusesMissingOrFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolveInbox(filepath.Join(dir, "nope")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing inbox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope")); err == nil {
		t.Error("ResolveInbox created the missing directory")
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := ResolveInbox(file); err == nil {
		t.Error("a file was accepted as the inbox")
	}
}

func TestResolveInboxFollowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "on-the-big-drive")
	link := filepath.Join(dir, "inbox")
	os.Mkdir(target, 0o700)
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	// Remove only the junction, never what it points at.
	t.Cleanup(func() { exec.Command("cmd", "/c", "rmdir", link).Run() })

	got, err := ResolveInbox(link)
	if err != nil {
		t.Fatal(err)
	}
	// Compare the directories, not their spellings: a temp path can use 8.3
	// short names (C:\Users\RUNNER~1\...) where Windows resolves to long ones.
	gotInfo, err1 := os.Stat(got)
	targetInfo, err2 := os.Stat(target)
	if err1 != nil || err2 != nil || !os.SameFile(gotInfo, targetInfo) {
		t.Errorf("resolved to %q, which is not %q", got, target)
	}
	if strings.EqualFold(filepath.Base(got), "inbox") {
		t.Errorf("resolved to %q, which is still the junction itself", got)
	}
}

func TestDiskFree(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("free space is not reported on this OS")
	}
	free, err := DiskFree(t.TempDir())
	if err != nil || free <= 0 {
		t.Errorf("DiskFree: %d, %v", free, err)
	}
}
