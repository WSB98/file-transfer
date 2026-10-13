package console

import (
	"strings"
	"testing"
	"time"
)

func TestStatusStaysBelowLogs(t *testing.T) {
	var out strings.Builder
	c := New(&out)
	c.SetStatus("receiving 10%")
	c.Logf("hello %s", "there")
	got := out.String()
	// The status is erased, the log line printed, then the status redrawn.
	if !strings.Contains(got, "\r             \r") || !strings.HasSuffix(got, "hello there\nreceiving 10%") {
		t.Errorf("output %q", got)
	}
}

func TestProgressLine(t *testing.T) {
	snap := Snapshot{ID: "a", Phase: "receiving", Name: strings.Repeat("a long holiday archive name ", 6) + ".zip", Size: 4000, Done: 1000}
	moving := true
	p := &Progress{Source: func(time.Time) (Snapshot, bool) { return snap, moving }}

	now := time.Now()
	line := p.Line(now)
	if !strings.HasPrefix(line, "receiving  25.0%") || !strings.Contains(line, "ETA --") || len(line) > Width {
		t.Errorf("first line %q (%d chars)", line, len(line))
	}
	snap.Moved, snap.Done = 500, 1500
	if line = p.Line(now.Add(time.Second)); !strings.Contains(line, "ETA 5s") {
		t.Errorf("after 500 B/s, line %q should estimate 5s left", line)
	}

	snap.ID, snap.Moved = "b", 9000
	if line = p.Line(now.Add(2 * time.Second)); !strings.Contains(line, "ETA --") {
		t.Errorf("a different upload must restart the rate, got %q", line)
	}

	moving = false
	if p.Line(now.Add(time.Minute)) != "" {
		t.Error("nothing moving should clear the line")
	}
}
