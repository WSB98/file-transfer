// Command drop receives one or more large files from a friend's browser,
// straight onto this computer, over HTTPS with a secret link and a key.
//
// Run it, send the printed link and key, and press Ctrl+C when done. See the
// README for the one-time setup (an inbox folder, and a VPN or a port forward).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/WSB98/file-transfer/internal/console"
	"github.com/WSB98/file-transfer/internal/creds"
	"github.com/WSB98/file-transfer/internal/human"
	"github.com/WSB98/file-transfer/internal/server"
	"github.com/WSB98/file-transfer/internal/store"
)

// version is set by release builds (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if errors.Is(err, errVersion) {
		fmt.Println("drop", version)
		return
	}
	if errors.Is(err, errNoAddr) {
		printAdapters(os.Stderr, adapterAddrs())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "drop:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	c, err := parseConfig(args, os.Stderr)
	if err != nil {
		return err
	}
	dir, err := store.ResolveInbox(c.inbox)
	if err != nil {
		return err
	}

	con := console.New(os.Stdout)
	st, err := store.New(store.Options{
		Inbox:     dir,
		ChunkSize: c.chunkMiB << 20,
		MaxTotal:  c.maxGiB << 30,
		Reserve:   c.keepGiB << 30,
		Logf:      con.Logf,
	})
	if err != nil {
		return err
	}
	defer st.Close()

	token, err := creds.NewToken()
	if err != nil {
		return err
	}
	key, err := creds.NewKey()
	if err != nil {
		return err
	}
	hosts := []string{"127.0.0.1", "::1", "localhost"}
	if c.host != "" {
		hosts = append(hosts, c.host)
	}
	cert, err := creds.NewCert(hosts, time.Now())
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		return err
	}

	srv := server.HTTPServer(server.New(server.Config{
		Token:    token,
		Key:      key,
		Store:    st,
		Parallel: c.parallel,
		Logf:     con.Logf,
	}).Handler(), cert)

	printBanner(c, ln.Addr().(*net.TCPAddr), dir, token, key, creds.Fingerprint(cert), st.Unfinished())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go (&console.Progress{Source: progressSource(st), Console: con}).Run(ctx)
	errc := make(chan error, 1)
	go func() { errc <- srv.ServeTLS(ln, "", "") }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	con.Logf("stopping; anything unfinished resumes when drop starts again")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close()
	}
	return nil
}

// progressSource feeds the status line from whichever upload is moving.
func progressSource(st *store.Store) func(time.Time) (console.Snapshot, bool) {
	return func(now time.Time) (console.Snapshot, bool) {
		u := st.Active(now)
		if u == nil {
			return console.Snapshot{}, false
		}
		s := u.Status()
		done := s.Received
		if s.Phase == store.Verifying {
			done = s.Verified
		}
		return console.Snapshot{ID: u.ID(), Phase: string(s.Phase), Name: s.Name, Size: s.Size, Done: done, Moved: u.Moved()}, true
	}
}

func printBanner(c config, addr *net.TCPAddr, dir, token, key, fingerprint string, pending []store.Pending) {
	link := fmt.Sprintf("https://%s:%d/t/%s/", c.host, addr.Port, token)
	if c.host == "" {
		link = fmt.Sprintf("https://<your public IP>:%d/t/%s/  (pass -host to fill this in)", addr.Port, token)
	}
	saving := dir
	if abs, err := filepath.Abs(c.inbox); err == nil && abs != dir {
		saving += fmt.Sprintf("  (via %s)", c.inbox)
	}
	if free, err := store.DiskFree(dir); err == nil {
		saving += fmt.Sprintf(", %s free", human.Bytes(free))
	}

	fmt.Printf("drop %s is listening on %s (%s). Press Ctrl+C to stop.\n\n", version, addr, reachDescription(addr))
	fmt.Printf("  Lobby link:     %s\n", link)
	fmt.Printf("  Key:            %s   (send by a different channel than the link)\n", key)
	if addr.IP.IsUnspecified() {
		fmt.Printf("  On this PC:     https://127.0.0.1:%d/t/%s/\n", addr.Port, token)
	} else {
		fmt.Printf("  On this PC:     open the lobby link here too, with the same key, to watch\n")
	}
	fmt.Printf("  Certificate:    SHA-256 %s\n", fingerprint)
	fmt.Printf("  Saving to:      %s\n", saving)
	fmt.Printf("  Limits:         %d MiB chunks, %d in parallel, %d GiB total, %d GiB kept free\n", c.chunkMiB, c.parallel, c.maxGiB, c.keepGiB)
	if c.settings != "" {
		settings := c.settings
		if abs, err := filepath.Abs(settings); err == nil {
			settings = abs
		}
		fmt.Printf("  Settings:       %s  (flags on the command line override it)\n", settings)
	}
	for _, p := range pending {
		fmt.Printf("  Unfinished:     %s, %s of %s received; resumes when the same file is picked again\n", p.Name, human.Bytes(p.Received), human.Bytes(p.Size))
	}
	fmt.Println()
}
