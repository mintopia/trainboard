package config

import (
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/tz"
)

// at builds a London wall-clock instant, so callers describe the window
// boundaries they intend rather than a raw UTC instant that would drift
// across the BST transition.
func at(hhmm string) time.Time {
	t, _ := time.Parse("15:04", hhmm)
	return time.Date(2026, 7, 2, t.Hour(), t.Minute(), 0, 0, tz.Location())
}

func TestBrightnessDisabledAlwaysNormal(t *testing.T) {
	c := Default() // powersaving disabled
	if got := c.BrightnessAt(at("02:00")); got != NormalBrightness {
		t.Fatalf("disabled brightness = %d, want %d", got, NormalBrightness)
	}
}

func TestBrightnessCrossMidnightWindow(t *testing.T) {
	c := Default()
	c.Powersaving.Enabled = true
	c.Powersaving.Start = "23:00"
	c.Powersaving.End = "07:00"
	c.Powersaving.Brightness = 20
	inside := []string{"23:00", "23:30", "00:00", "03:00", "06:59"}
	outside := []string{"07:00", "07:30", "12:00", "22:59"}
	for _, s := range inside {
		if got := c.BrightnessAt(at(s)); got != 20 {
			t.Errorf("at %s brightness = %d, want 20 (inside)", s, got)
		}
	}
	for _, s := range outside {
		if got := c.BrightnessAt(at(s)); got != NormalBrightness {
			t.Errorf("at %s brightness = %d, want %d (outside)", s, got, NormalBrightness)
		}
	}
}

func TestBrightnessSameDayWindow(t *testing.T) {
	c := Default()
	c.Powersaving.Enabled = true
	c.Powersaving.Start = "01:00"
	c.Powersaving.End = "06:00"
	c.Powersaving.Brightness = 10
	if got := c.BrightnessAt(at("03:00")); got != 10 {
		t.Errorf("inside same-day window = %d, want 10", got)
	}
	if got := c.BrightnessAt(at("12:00")); got != NormalBrightness {
		t.Errorf("outside same-day window = %d, want %d", got, NormalBrightness)
	}
}

func TestBrightnessUnparseableTimeFailsSafe(t *testing.T) {
	c := Default()
	c.Powersaving.Enabled = true
	c.Powersaving.Brightness = 10

	c.Powersaving.Start = "garbage"
	c.Powersaving.End = "07:00"
	if got := c.BrightnessAt(at("03:00")); got != NormalBrightness {
		t.Fatalf("unparseable start: brightness = %d, want NormalBrightness %d", got, NormalBrightness)
	}

	c.Powersaving.Start = "23:00"
	c.Powersaving.End = "not-a-time"
	if got := c.BrightnessAt(at("03:00")); got != NormalBrightness {
		t.Fatalf("unparseable end: brightness = %d, want NormalBrightness %d", got, NormalBrightness)
	}
}

// TestBrightnessWindowEvaluatedInLondonWallClock checks the window is
// evaluated against Europe/London wall-clock time, not the instant's raw
// UTC clock. UK clocks spring forward at 2026-03-29 01:00:00 UTC (GMT +0 ->
// BST +1): a window of 00:30-01:30 London time must exclude the flip
// instant (London wall clock 02:00) even though its UTC hour (01:00) would
// still read as inside the window.
func TestBrightnessWindowEvaluatedInLondonWallClock(t *testing.T) {
	c := Default()
	c.Powersaving.Enabled = true
	c.Powersaving.Start = "00:30"
	c.Powersaving.End = "01:30"
	c.Powersaving.Brightness = 5

	preFlip := time.Date(2026, 3, 29, 0, 59, 0, 0, time.UTC) // London: 00:59, inside
	if got := c.BrightnessAt(preFlip); got != 5 {
		t.Errorf("at %s (pre-flip) brightness = %d, want 5 (inside)", preFlip, got)
	}

	postFlip := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC) // London: 02:00 BST, outside
	if got := c.BrightnessAt(postFlip); got != NormalBrightness {
		t.Errorf("at %s (post-flip) brightness = %d, want %d (outside)", postFlip, got, NormalBrightness)
	}
}
