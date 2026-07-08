package render

import (
	"bytes"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/tz"
)

func TestClockGolden(t *testing.T) {
	fb := New(256, 14)
	c := &Clock{Large: mustFont(t, BoldTTF, 20), Tall: mustFont(t, BoldTallTTF, 10), W: 256, Level: 15}
	c.Render(fb, 0, time.Date(2026, 7, 2, 12, 34, 56, 0, tz.Location()))
	assertGolden(t, "clock_123456", fb)
}

func TestClockIsCentered(t *testing.T) {
	fb := New(256, 14)
	c := &Clock{Large: mustFont(t, BoldTTF, 20), Tall: mustFont(t, BoldTallTTF, 10), W: 256, Level: 15}
	c.Render(fb, 0, time.Date(2026, 7, 2, 12, 34, 56, 0, tz.Location()))
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

// TestClockRendersLondonWallClockAcrossBSTBoundary checks the clock renders
// Europe/London wall-clock time, not the raw instant's UTC clock. UK clocks
// spring forward at 2026-03-29 01:00:00 UTC (GMT +0 -> BST +1): the instant
// just before renders as 00:59, the instant of the flip renders as 02:00.
func TestClockRendersLondonWallClockAcrossBSTBoundary(t *testing.T) {
	newClock := func() *Clock {
		return &Clock{Large: mustFont(t, BoldTTF, 20), Tall: mustFont(t, BoldTallTTF, 10), W: 256, Level: 15}
	}
	render := func(when time.Time) *Framebuffer {
		fb := New(256, 14)
		newClock().Render(fb, 0, when)
		return fb
	}

	preFlip := time.Date(2026, 3, 29, 0, 59, 0, 0, time.UTC) // GMT: wall clock 00:59
	postFlip := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC) // BST: wall clock 02:00
	wantPre := render(time.Date(2026, 3, 29, 0, 59, 0, 0, tz.Location()))
	wantPost := render(time.Date(2026, 3, 29, 2, 0, 0, 0, tz.Location()))

	if got := render(preFlip); !bytes.Equal(got.Pix, wantPre.Pix) {
		t.Fatalf("clock at %s did not render as London 00:59 (pre-flip GMT)", preFlip)
	}
	if got := render(postFlip); !bytes.Equal(got.Pix, wantPost.Pix) {
		t.Fatalf("clock at %s did not render as London 02:00 (post-flip BST)", postFlip)
	}
	if got := render(preFlip); bytes.Equal(got.Pix, wantPost.Pix) {
		t.Fatalf("clock at %s rendered identically pre/post BST flip; test is not discriminating", preFlip)
	}
}
