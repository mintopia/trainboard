package config

import (
	"testing"
	"time"
)

func at(hhmm string) time.Time {
	t, _ := time.Parse("15:04", hhmm)
	return time.Date(2026, 7, 2, t.Hour(), t.Minute(), 0, 0, time.UTC)
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
