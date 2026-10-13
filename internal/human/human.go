// Package human formats sizes, rates, and durations for people to read.
// Sizes are decimal (1 GB = 10^9 bytes), the way drive makers and ISPs count.
package human

import (
	"fmt"
	"time"
)

// Bytes formats a byte count, such as "200.0 GB".
func Bytes(n int64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.2f TB", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Rate formats a transfer rate, such as "12.5 MB/s".
func Rate(bytesPerSec float64) string {
	return fmt.Sprintf("%.1f MB/s", bytesPerSec/1e6)
}

// Duration formats a time left, such as "3h05m", "12m30s", or "9s".
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
