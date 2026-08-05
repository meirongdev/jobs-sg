package mcf

import (
	"testing"
	"time"
)

func TestParsePostingDate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"2026-08-03", time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), true}, // live API format
		{"2026-08-03T00:00:00Z", time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), true},
		{"", time.Time{}, false},
		{"03/08/2026", time.Time{}, false},
	} {
		got, err := ParsePostingDate(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("ParsePostingDate(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && !got.Equal(tc.want) {
			t.Errorf("ParsePostingDate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
