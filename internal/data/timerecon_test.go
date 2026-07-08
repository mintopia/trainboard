package data

import (
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/tz"
)

func TestReconstructTimesSameDay(t *testing.T) {
	loc := tz.Location()
	b := &Board{
		GeneratedAt: time.Date(2026, 7, 2, 12, 30, 0, 0, loc),
		Departures:  []Departure{{ScheduledTime: "12:45"}},
	}
	reconstructTimes(b, loc)
	want := time.Date(2026, 7, 2, 12, 45, 0, 0, loc)
	if !b.Departures[0].When.Equal(want) {
		t.Fatalf("When = %v, want %v", b.Departures[0].When, want)
	}
}

func TestReconstructTimesRollsPastMidnight(t *testing.T) {
	loc := tz.Location()
	// Board generated at 23:50; a 00:12 service is tomorrow.
	b := &Board{
		GeneratedAt: time.Date(2026, 7, 2, 23, 50, 0, 0, loc),
		Departures:  []Departure{{ScheduledTime: "00:12"}},
	}
	reconstructTimes(b, loc)
	want := time.Date(2026, 7, 3, 0, 12, 0, 0, loc)
	if !b.Departures[0].When.Equal(want) {
		t.Fatalf("When = %v, want %v", b.Departures[0].When, want)
	}
}

func TestReconstructTimesRecentPastStaysToday(t *testing.T) {
	loc := tz.Location()
	// A std slightly before generatedAt (within 6h) is the same day, not tomorrow.
	b := &Board{
		GeneratedAt: time.Date(2026, 7, 2, 12, 30, 0, 0, loc),
		Departures:  []Departure{{ScheduledTime: "12:28"}},
	}
	reconstructTimes(b, loc)
	want := time.Date(2026, 7, 2, 12, 28, 0, 0, loc)
	if !b.Departures[0].When.Equal(want) {
		t.Fatalf("When = %v, want %v", b.Departures[0].When, want)
	}
}

func TestReconstructTimesDSTSpringForward(t *testing.T) {
	loc := tz.Location()
	// 2026 UK clocks go forward on 29 March. A board late on the 28th with an
	// early-hours service on the 29th must land in BST (offset +1h).
	b := &Board{
		GeneratedAt: time.Date(2026, 3, 28, 23, 40, 0, 0, loc),
		Departures:  []Departure{{ScheduledTime: "02:30"}},
	}
	reconstructTimes(b, loc)
	got := b.Departures[0].When
	_, offset := got.Zone()
	if got.Day() != 29 || offset != 3600 {
		t.Fatalf("DST reconstruction = %v (offset %ds), want 29th at +3600s", got, offset)
	}
}
