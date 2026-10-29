// Package reminder decides when to remind the user to write in their diary.
package reminder

import "time"

// Config is the daily reminder setting. It is stored with the UI state.
type Config struct {
	Enabled bool `json:"enabled"`
	Hour    int  `json:"hour"`
	Minute  int  `json:"minute"`
	// LastDone is the local day (YYYY-MM-DD) the reminder was shown, or an
	// entry for that day was saved, so it is shown at most once a day and not
	// after writing, even across restarts.
	LastDone string `json:"last_done,omitempty"`
}

// Default is the setting used until the user changes it: every day at 20:00.
var Default = Config{Enabled: true, Hour: 20}

// Day returns the key used for LastDone.
func Day(t time.Time) string { return t.Format(time.DateOnly) }

// Due reports whether the reminder should be shown at now: it is enabled,
// today's reminder time has passed and today is not done yet.
// A reminder missed while the app was not running is shown later that day.
func (c Config) Due(now time.Time) bool {
	if !c.Enabled || c.LastDone == Day(now) {
		return false
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), c.Hour, c.Minute, 0, 0, now.Location())
	return !now.Before(at)
}
