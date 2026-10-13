// Package console owns the terminal: log lines scroll above one status line
// that is redrawn in place.
package console

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/WSB98/file-transfer/internal/human"
)

// Width keeps the status line inside an 80-column terminal, since a wrapped
// line can't be redrawn in place.
const Width = 79

// Console writes log lines and a status line. Plain carriage returns and space
// padding work in every terminal, Windows included, with no escape codes.
type Console struct {
	mu     sync.Mutex
	out    io.Writer
	status string
}

func New(out io.Writer) *Console {
	return &Console{out: out}
}

// Logf prints a timestamped line above the status line.
func (c *Console) Logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearStatus()
	fmt.Fprintf(c.out, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	fmt.Fprint(c.out, c.status)
}

// SetStatus replaces the status line; "" removes it.
func (c *Console) SetStatus(s string) {
	if len(s) > Width {
		s = s[:Width]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearStatus()
	c.status = s
	fmt.Fprint(c.out, c.status)
}

func (c *Console) clearStatus() {
	if c.status != "" {
		fmt.Fprintf(c.out, "\r%s\r", strings.Repeat(" ", len(c.status)))
	}
}

// Snapshot is what the progress line shows about one upload.
type Snapshot struct {
	ID    string
	Phase string
	Name  string
	Size  int64
	Done  int64 // bytes received, or read back while verifying
	Moved int64 // a counter that grows as bytes move, for the rate
}

// Progress redraws the status line once a second from Source, with a rate
// averaged over the last ten seconds.
type Progress struct {
	Source  func(now time.Time) (Snapshot, bool)
	Console *Console

	follow  string
	samples []int64
}

func (p *Progress) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			p.Console.SetStatus("")
			return
		case now := <-tick.C:
			p.Console.SetStatus(p.Line(now))
		}
	}
}

// Line renders the status line for now, or "" when nothing is moving.
func (p *Progress) Line(now time.Time) string {
	s, ok := p.Source(now)
	if !ok {
		p.follow, p.samples = "", nil
		return ""
	}
	if s.ID != p.follow {
		p.follow, p.samples = s.ID, nil
	}
	p.samples = append(p.samples, s.Moved)
	if len(p.samples) > 11 {
		p.samples = p.samples[1:]
	}
	var rate float64
	if n := len(p.samples); n > 1 {
		rate = float64(p.samples[n-1]-p.samples[0]) / float64(n-1)
	}

	pct := 100 * float64(s.Done) / float64(s.Size)
	eta := "--"
	if rate > 0 {
		eta = human.Duration(time.Duration(float64(s.Size-s.Done) / rate * float64(time.Second)))
	}
	head := fmt.Sprintf("%s %5.1f%%  %s of %s  %s  ETA %s  ",
		s.Phase, pct, human.Bytes(s.Done), human.Bytes(s.Size), human.Rate(rate), eta)
	room := Width - len(head)
	if room < 4 {
		return head
	}
	name := s.Name
	if len(name) > room {
		name = name[:room-3] + "..."
	}
	return head + name
}
