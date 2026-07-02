package render

import (
	"testing"
	"time"
)

func TestClockGolden(t *testing.T) {
	fb := New(256, 14)
	c := &Clock{Large: mustFont(t, BoldTTF, 20), Tall: mustFont(t, BoldTallTTF, 10), W: 256, Level: 15}
	c.Render(fb, 0, time.Date(2026, 7, 2, 12, 34, 56, 0, time.UTC))
	assertGolden(t, "clock_123456", fb)
}

func TestClockIsCentered(t *testing.T) {
	fb := New(256, 14)
	c := &Clock{Large: mustFont(t, BoldTTF, 20), Tall: mustFont(t, BoldTallTTF, 10), W: 256, Level: 15}
	c.Render(fb, 0, time.Date(2026, 7, 2, 12, 34, 56, 0, time.UTC))
	// Ink should be roughly centered: left margin ≈ right margin (±8px).
	var minX, maxX = fb.W, 0
	for x := 0; x < fb.W; x++ {
		for y := 0; y < fb.H; y++ {
			if fb.At(x, y) > 0 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
			}
		}
	}
	left := minX
	right := fb.W - 1 - maxX
	if diff := left - right; diff > 8 || diff < -8 {
		t.Fatalf("clock not centered: leftMargin=%d rightMargin=%d", left, right)
	}
}
