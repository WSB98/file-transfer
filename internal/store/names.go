package store

import (
	"path/filepath"
	"strings"
)

const maxNameLen = 200

// sanitizeName maps a client-supplied file name onto a conservative ASCII
// subset that is safe on every filesystem. It never fails: a hostile or odd
// name degrades to something harmless instead of blocking a 200 GB upload.
// Path separators can't survive the mapping, so the result is always a single
// path element.
func sanitizeName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if isSafeNameRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), " .")

	if len(name) > maxNameLen {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		name = strings.TrimRight(name[:maxNameLen-len(ext)], " .") + ext
	}
	if name == "" {
		name = "upload"
	}
	if isReservedWindowsName(name) {
		name = "_" + name
	}
	return name
}

func isSafeNameRune(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return true
	}
	return strings.ContainsRune(" ._()-", r)
}

// Windows treats these device names as reserved no matter the extension or
// trailing spaces: "CON.zip" and "nul .txt" both open a device, not a file.
var reservedWindowsNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM0": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT0": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
	"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func isReservedWindowsName(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	return reservedWindowsNames[strings.ToUpper(strings.TrimRight(stem, " "))]
}
