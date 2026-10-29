package reminder

import (
	"testing"
	"time"
)

func TestDue(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 29, h, m, 0, 0, time.Local) }
	tests := []struct {
		name string
		cfg  Config
		now  time.Time
		want bool
	}{
		{"before time", Default, at(19, 59), false},
		{"at time", Default, at(20, 0), true},
		{"later the same day", Default, at(23, 30), true},
		{"disabled", Config{Hour: 20}, at(21, 0), false},
		{"done today", Config{Enabled: true, Hour: 20, LastDone: "2026-10-29"}, at(21, 0), false},
		{"done yesterday", Config{Enabled: true, Hour: 20, LastDone: "2026-10-28"}, at(21, 0), true},
		{"minutes count", Config{Enabled: true, Hour: 8, Minute: 30}, at(8, 29), false},
	}
	for _, tt := range tests {
		if got := tt.cfg.Due(tt.now); got != tt.want {
			t.Errorf("%s: Due = %v, want %v", tt.name, got, tt.want)
		}
	}
}
