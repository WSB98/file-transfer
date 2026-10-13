package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"backup.zip", "backup.zip"},
		{"My Photos (2024)-final.zip", "My Photos (2024)-final.zip"},
		{"../../Windows/System32/x.dll", "_.._Windows_System32_x.dll"},
		{`..\..\boot.ini`, `_.._boot.ini`},
		{`C:\Users\x\a.zip`, "C__Users_x_a.zip"},
		{`\\server\share\a.zip`, "__server_share_a.zip"},
		{"/etc/passwd", "_etc_passwd"},
		{"..", "upload"},
		{"", "upload"},
		{" . ", "upload"},
		{".bashrc", "bashrc"},
		{"name. ", "name"},
		{"a.zip:hidden", "a.zip_hidden"},
		{"tab\there\x00nul", "tab_here_nul"},
		{"日本語.zip", "___.zip"},
		{"CON", "_CON"},
		{"con.zip", "_con.zip"},
		{"NUL .txt", "_NUL .txt"},
		{"lpt9.tar.gz", "_lpt9.tar.gz"},
		{"CONSOLE.zip", "CONSOLE.zip"},
	}
	for _, c := range cases {
		if got := sanitizeName(c.in); got != c.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeNameTruncatesKeepingExtension(t *testing.T) {
	got := sanitizeName(strings.Repeat("a", 500) + ".zip")
	if len(got) != maxNameLen || !strings.HasSuffix(got, ".zip") {
		t.Errorf("got %d bytes ending %q", len(got), got[len(got)-8:])
	}
}

func FuzzSanitizeName(f *testing.F) {
	for _, s := range []string{"a.zip", "../x", `..\..\y`, "CON", " .", "日本", strings.Repeat("x.", 300)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := sanitizeName(in)
		switch {
		case got == "":
			t.Fatal("empty")
		case len(got) > maxNameLen+1: // +1 for a reserved-name prefix
			t.Fatalf("%d bytes", len(got))
		case filepath.Base(got) != got || strings.ContainsAny(got, `/\:`):
			t.Fatalf("%q is not a single path element", got)
		case strings.Trim(got, " .") != got && !strings.HasPrefix(got, "_"):
			t.Fatalf("%q has a leading or trailing dot or space", got)
		case isReservedWindowsName(got):
			t.Fatalf("%q is reserved", got)
		}
		for _, r := range got {
			if !isSafeNameRune(r) {
				t.Fatalf("%q contains %q", got, r)
			}
		}
	})
}
