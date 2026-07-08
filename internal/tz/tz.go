// Package tz provides the single Europe/London *time.Location used
// everywhere the board displays or compares wall-clock time. The board is a
// fixed UK installation, so the display timezone is never configurable —
// only the host's system clock offset varies (some images ship UTC-only,
// with no zoneinfo database at all).
package tz

import (
	"log/slog"
	"sync"
	"time"

	_ "time/tzdata" // embed the zoneinfo database so Location() never depends on host zoneinfo
)

var (
	once sync.Once
	loc  *time.Location
)

// Location returns Europe/London, resolving via the embedded tzdata when the
// host's zoneinfo database is missing or incomplete. It never panics: on any
// resolution error it logs once via slog.Default() and falls back to
// time.UTC.
func Location() *time.Location {
	once.Do(func() {
		l, err := time.LoadLocation("Europe/London")
		if err != nil {
			slog.Default().Error("tz: failed to load Europe/London, falling back to UTC", "err", err.Error())
			loc = time.UTC
			return
		}
		loc = l
	})
	return loc
}
