package human

import (
	"testing"
	"time"
)

func TestFormatting(t *testing.T) {
	cases := []struct{ got, want string }{
		{Bytes(200e9), "200.0 GB"},
		{Bytes(1_500_000_000_000), "1.50 TB"},
		{Bytes(70e6), "70.0 MB"},
		{Bytes(12), "12 B"},
		{Rate(11_200_000), "11.2 MB/s"},
		{Duration(3*time.Hour + 5*time.Minute + 40*time.Second), "3h05m"},
		{Duration(12*time.Minute + 30*time.Second), "12m30s"},
		{Duration(9 * time.Second), "9s"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
