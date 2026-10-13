package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
)

// config is everything the command line and drop.json can set.
type config struct {
	addr     string
	host     string
	inbox    string
	chunkMiB int64
	parallel int
	maxGiB   int64
	keepGiB  int64
	settings string // the settings file that was read, or ""
}

const (
	// The server holds up to two chunks per parallel slot in memory at once.
	memoryBudgetMiB = 1024
	// Browsers open at most 6 connections to one server, and the lobby needs
	// one. A seventh chunk would queue behind the others and trip the page's
	// stall watchdog.
	maxParallel = 5
	// settingsName is read from the current folder when -config isn't given.
	settingsName = "drop.json"
)

var (
	// errNoAddr asks the user to choose where drop listens. Picking for them
	// could put it on the internet when they meant a VPN.
	errNoAddr = errors.New("choose where to listen with -addr")
	// errVersion asks main to print the version and stop.
	errVersion = errors.New("version requested")
)

func parseConfig(args []string, stderr io.Writer) (config, error) {
	var c config
	flags := flag.NewFlagSet("drop", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&c.addr, "addr", "", "address to listen on: a VPN address (100.64.1.2:8443), :8443 for every adapter (port forward), or 127.0.0.1:8443 for this computer only; run without it to list this computer's addresses")
	flags.StringVar(&c.host, "host", "", "address or name the sender will use, for the link and certificate (default: the -addr address)")
	flags.StringVar(&c.inbox, "inbox", "inbox", "existing directory, junction, or symlink that receives files")
	flags.Int64Var(&c.chunkMiB, "chunk", 32, "chunk size in MiB (1-256)")
	flags.IntVar(&c.parallel, "parallel", 4, fmt.Sprintf("chunks the browser sends at once (1-%d)", maxParallel))
	flags.Int64Var(&c.maxGiB, "max", 512, "most GiB this run accepts, across all files")
	flags.Int64Var(&c.keepGiB, "keep-free", 10, "GiB to always leave free on the inbox's drive")
	settings := flags.String("config", "", "settings file with the same names as these flags (default: "+settingsName+" in the current folder, if it exists)")
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() > 0 {
		return config{}, fmt.Errorf("unexpected argument %q; see -h", flags.Arg(0))
	}
	if *showVersion {
		return config{}, errVersion
	}

	onCommandLine := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { onCommandLine[f.Name] = true })
	path, required := *settings, true
	if path == "" {
		path, required = settingsName, false
	}
	found, err := applySettings(flags, path, onCommandLine)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !required:
	case err != nil:
		return config{}, err
	case found:
		c.settings = path
	}

	switch {
	case c.chunkMiB < 1 || c.chunkMiB > 256:
		return config{}, errors.New("-chunk must be 1 to 256")
	case c.parallel < 1 || c.parallel > maxParallel:
		return config{}, fmt.Errorf("-parallel must be 1 to %d: browsers open at most 6 connections to one server, and the lobby uses one", maxParallel)
	case c.chunkMiB*int64(c.parallel)*2 > memoryBudgetMiB:
		return config{}, fmt.Errorf("-chunk %d with -parallel %d could hold %d MiB in memory; keep chunk x parallel x 2 within %d", c.chunkMiB, c.parallel, c.chunkMiB*int64(c.parallel)*2, memoryBudgetMiB)
	case c.maxGiB < 1 || c.maxGiB > 1<<20:
		return config{}, errors.New("-max must be 1 to 1048576 GiB")
	case c.keepGiB < 0 || c.keepGiB > 1<<20:
		return config{}, errors.New("-keep-free must be 0 to 1048576 GiB")
	case c.addr == "":
		return config{}, errNoAddr
	}
	ip, _, err := net.SplitHostPort(c.addr)
	if err != nil {
		return config{}, fmt.Errorf("-addr %q: %v", c.addr, err)
	}
	if c.host == "" {
		if parsed := net.ParseIP(ip); parsed != nil && !parsed.IsUnspecified() {
			c.host = ip
		}
	}
	return c, nil
}

// applySettings reads a settings file, a JSON object whose keys are flag
// names, and sets each flag the command line didn't. Every value goes
// through the flag's own parser, so the file and the command line can't
// disagree about what is valid. An unknown key is an error, not a silent
// typo. It reports whether the file existed.
func applySettings(flags *flag.FlagSet, path string, onCommandLine map[string]bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return true, fmt.Errorf("%s: %v", path, err)
	}
	for name, raw := range values {
		f := flags.Lookup(name)
		if f == nil || name == "config" || name == "version" {
			return true, fmt.Errorf("%s: unknown setting %q; the settings are the flags listed by -h", path, name)
		}
		if onCommandLine[name] {
			continue
		}
		value, err := settingText(raw)
		if err != nil {
			return true, fmt.Errorf("%s: %q: %v", path, name, err)
		}
		// A relative inbox in the file is relative to the file, so the file
		// means the same thing whichever folder drop is started from.
		if name == "inbox" && value != "" && !filepath.IsAbs(value) {
			value = filepath.Join(filepath.Dir(path), value)
		}
		if err := flags.Set(name, value); err != nil {
			return true, fmt.Errorf("%s: %q: %v", path, name, err)
		}
	}
	return true, nil
}

// settingText turns a JSON string or number into the text a flag parses.
func settingText(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	switch v := v.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	default:
		return "", errors.New("must be a string or a number")
	}
}
