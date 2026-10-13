package main

import (
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	if _, err := parseConfig(nil, io.Discard); !errors.Is(err, errNoAddr) {
		t.Fatalf("no -addr: %v, want errNoAddr", err)
	}
	c, err := parseConfig([]string{"-addr", "100.101.102.103:8443"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := config{addr: "100.101.102.103:8443", host: "100.101.102.103", inbox: "inbox", chunkMiB: 32, parallel: 4, maxGiB: 512, keepGiB: 10}
	if c != want {
		t.Errorf("defaults %+v, want %+v", c, want)
	}
	if _, err := parseConfig([]string{"-version"}, io.Discard); !errors.Is(err, errVersion) {
		t.Errorf("-version: %v, want errVersion", err)
	}
}

func TestHostDefaultsToASpecificAddressOnly(t *testing.T) {
	for _, c := range []struct {
		args []string
		host string
	}{
		{[]string{"-addr", "127.0.0.1:9000"}, "127.0.0.1"},
		{[]string{"-addr", ":8443"}, ""},
		{[]string{"-addr", "0.0.0.0:8443"}, ""},
		{[]string{"-addr", ":8443", "-host", "203.0.113.7"}, "203.0.113.7"},
		{[]string{"-addr", "100.64.1.2:8443", "-host", "drop.example"}, "drop.example"},
	} {
		got, err := parseConfig(c.args, io.Discard)
		if err != nil || got.host != c.host {
			t.Errorf("%v: host %q, err %v; want %q", c.args, got.host, err, c.host)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string // "" means valid
	}{
		{[]string{"-addr", "127.0.0.1:9000", "-host", "203.0.113.7", "-inbox", `D:\drop-inbox`}, ""},
		{[]string{"-addr", ":8443", "--parallel", "5", "--chunk", "64"}, ""},
		{[]string{"-addr", ":8443", "-keep-free", "0"}, ""},
		{[]string{"-chunk", "0"}, "-chunk"},
		{[]string{"-chunk", "257"}, "-chunk"},
		{[]string{"-parallel", "0"}, "-parallel"},
		{[]string{"-parallel", "6"}, "at most 6 connections"},
		{[]string{"-chunk", "128", "-parallel", "5"}, "in memory"},
		{[]string{"-max", "0"}, "-max"},
		{[]string{"-keep-free", "-1"}, "-keep-free"},
		{[]string{"-addr", "8443"}, "-addr"},
		{[]string{"stray"}, "unexpected argument"},
		{[]string{"-nope"}, "not defined"},
	} {
		_, err := parseConfig(c.args, io.Discard)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%v: unexpected error %v", c.args, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%v: got %v, want an error mentioning %q", c.args, err, c.want)
		}
	}
	if _, err := parseConfig([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
}

func writeSettings(t *testing.T, json string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSettingsFileFillsWhatTheCommandLineDoesNot(t *testing.T) {
	path := writeSettings(t, `{"addr": ":9000", "host": "203.0.113.7", "inbox": "files", "chunk": 64, "parallel": 2, "max": 100, "keep-free": 0}`)
	c, err := parseConfig([]string{"-config", path}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := config{addr: ":9000", host: "203.0.113.7", inbox: filepath.Join(filepath.Dir(path), "files"), chunkMiB: 64, parallel: 2, maxGiB: 100, keepGiB: 0, settings: path}
	if c != want {
		t.Errorf("from the file: %+v, want %+v", c, want)
	}

	c, err = parseConfig([]string{"-config", path, "-chunk", "16", "-inbox", "here"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.chunkMiB != 16 || c.inbox != "here" || c.parallel != 2 {
		t.Errorf("the command line should win: chunk %d, inbox %q, parallel %d", c.chunkMiB, c.inbox, c.parallel)
	}
}

func TestSettingsFileKeepsAnAbsoluteInbox(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "inbox")
	path := writeSettings(t, `{"addr": ":9000", "inbox": `+jsonString(abs)+`}`)
	c, err := parseConfig([]string{"-config", path}, io.Discard)
	if err != nil || c.inbox != abs {
		t.Errorf("inbox %q, err %v; want %q", c.inbox, err, abs)
	}
}

func TestSettingsFileMistakesAreErrors(t *testing.T) {
	for _, c := range []struct {
		json string
		want string
	}{
		{`{"chunks": 64}`, `unknown setting "chunks"`},
		{`{"config": "other.json"}`, `unknown setting "config"`},
		{`{"version": true}`, `unknown setting "version"`},
		{`{"parallel": "many"}`, `"parallel"`},
		{`{"parallel": 2.5}`, `"parallel"`},
		{`{"parallel": 9, "addr": ":9000"}`, "-parallel must be"},
		{`{"addr": true}`, "string or a number"},
		{`{"addr": null}`, "string or a number"},
		{`{"addr": [":9000"]}`, "string or a number"},
		{`[":9000"]`, "settings.json"},
		{`{"addr": ":9000",}`, "settings.json"},
		{``, "settings.json"},
	} {
		_, err := parseConfig([]string{"-config", writeSettings(t, c.json)}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error mentioning %q", c.json, err, c.want)
		}
	}
}

func TestTheExampleSettingsFileIsValid(t *testing.T) {
	example := filepath.Join("..", "..", "drop.example.json")
	c, err := parseConfig([]string{"-config", example}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != ":8443" || c.parallel != 4 {
		t.Errorf("example: %+v", c)
	}
}

func TestMissingSettingsFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	if _, err := parseConfig([]string{"-config", missing, "-addr", ":9000"}, io.Discard); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a named file that is missing: %v, want ErrNotExist", err)
	}
}

func TestDefaultSettingsFileIsReadFromTheCurrentFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settingsName), []byte(`{"addr": "127.0.0.1:9000"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inFolder(t, dir)
	c, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != "127.0.0.1:9000" || c.settings != settingsName || c.inbox != "inbox" {
		t.Errorf("addr %q, settings %q, inbox %q", c.addr, c.settings, c.inbox)
	}
}

func TestNoDefaultSettingsFileIsFine(t *testing.T) {
	inFolder(t, t.TempDir())
	c, err := parseConfig([]string{"-addr", ":9000"}, io.Discard)
	if err != nil || c.settings != "" {
		t.Errorf("settings %q, err %v", c.settings, err)
	}
}

// inFolder runs the rest of the test from dir.
func inFolder(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
