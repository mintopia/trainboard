package data

import (
	"sync"
	"time"
)

var (
	londonOnce sync.Once
	londonLoc  *time.Location
	londonErr  error
)

// londonLocation loads Europe/London once (needed for DST-correct times).
func londonLocation() (*time.Location, error) {
	londonOnce.Do(func() { londonLoc, londonErr = time.LoadLocation("Europe/London") })
	return londonLoc, londonErr
}

// reconstructTimes fills each departure's When from its "HH:MM" ScheduledTime,
// anchored to the board's GeneratedAt in loc. LDBWS gives no date, so a std
// more than 6h before generatedAt is treated as the next day (rolled past
// midnight); everything else is the same day.
func reconstructTimes(b *Board, loc *time.Location) {
	gen := b.GeneratedAt.In(loc)
	for i := range b.Departures {
		hhmm := b.Departures[i].ScheduledTime
		if len(hhmm) < 5 {
			continue
		}
		var h, m int
		if _, err := parseHHMM(hhmm, &h, &m); err != nil {
			continue
		}
		cand := time.Date(gen.Year(), gen.Month(), gen.Day(), h, m, 0, 0, loc)
		if cand.Before(gen.Add(-6 * time.Hour)) {
			cand = cand.AddDate(0, 0, 1)
		}
		b.Departures[i].When = cand
	}
}

// parseHHMM parses "HH:MM" (ignoring any trailing seconds) into h and m.
func parseHHMM(s string, h, m *int) (int, error) {
	t, err := time.Parse("15:04", s[:5])
	if err != nil {
		return 0, err
	}
	*h, *m = t.Hour(), t.Minute()
	return 2, nil
}
