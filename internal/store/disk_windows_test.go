package store

import "testing"

// TestFinishInAVeryLongPath passes on machines with LongPathsEnabled whether
// or not longPath is used, so the conversion itself is checked here.
func TestLongPathForm(t *testing.T) {
	for in, want := range map[string]string{
		`D:\drop-inbox\a.zip`:        `\\?\D:\drop-inbox\a.zip`,
		`D:\drop-inbox\.\x\..\a.zip`: `\\?\D:\drop-inbox\a.zip`,
		`\\server\share\inbox\a.zip`: `\\?\UNC\server\share\inbox\a.zip`,
		`\\?\D:\already\long`:        `\\?\D:\already\long`,
		`relative\a.zip`:             `relative\a.zip`,
	} {
		if got := longPath(in); got != want {
			t.Errorf("longPath(%q) = %q, want %q", in, got, want)
		}
	}
}
