package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func fbFor(t *testing.T, el render.Element, tick int) *render.Framebuffer {
	t.Helper()
	fb := render.New(W, H)
	el.Render(fb, tick, fixedNow)
	return fb
}

func TestOffsetElementPlacesClock(t *testing.T) {
	f := mustFonts(t)
	clock := offsetElement(&render.Clock{Large: f.BoldLarge, Tall: f.BoldTall, W: W, Level: 15}, 0, ClockY, W, ClockH)
	rendertest.AssertGolden(t, "testdata", "el_clock_at_50", fbFor(t, clock, 0))
}

func TestNextServiceScrollIn(t *testing.T) {
	f := mustFonts(t)
	el := newNextServiceRow(fixtureBoard().Departures[0], f)
	// Mid scroll-in: t=2 → 6 rows visible at y=6.
	rendertest.AssertGolden(t, "testdata", "el_next_t2", fbFor(t, el, 2))
	// Fully in: identical frames at t=5 and t=500.
	at5 := fbFor(t, el, 5)
	at500 := fbFor(t, el, 500)
	if string(at5.Pix) != string(at500.Pix) {
		t.Fatal("next-service row must be static once fully scrolled in")
	}
	rendertest.AssertGolden(t, "testdata", "el_next_full", at5)
}

func TestNextServiceMidScrollShowsTopSliceAtBottom(t *testing.T) {
	f := mustFonts(t)
	el := newNextServiceRow(fixtureBoard().Departures[0], f)
	fb := fbFor(t, el, 0) // b=2 → scratch rows 0..1 at y=10..11
	for y := 0; y < 10; y++ {
		for x := 0; x < W; x++ {
			if fb.At(x, y) != 0 {
				t.Fatalf("pixel (%d,%d) lit during first scroll tick", x, y)
			}
		}
	}
}

func TestRemainingServicesEmptyRendersNothing(t *testing.T) {
	f := mustFonts(t)
	fb := fbFor(t, newRemainingServices(nil, f), 100)
	for i, p := range fb.Pix {
		if p != 0 {
			t.Fatalf("pixel %d lit for empty remaining services", i)
		}
	}
}

func TestRemainingServicesHoldsRowDuringPause(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:] // ordinals 2..5
	el := newRemainingServices(deps, f)
	// After scroll-in (t=6) the window holds row 1 (first remaining, ordinal
	// "2nd") for rsPauseTicks. Frames at t=6 and t=6+124 must be identical.
	a := fbFor(t, el, 6)
	b := fbFor(t, el, 6+rsPauseTicks-1)
	if string(a.Pix) != string(b.Pix) {
		t.Fatal("carousel must hold the row for the whole pause")
	}
	rendertest.AssertGolden(t, "testdata", "el_remaining_hold2nd", a)
}

func TestRemainingServicesAdvancesToNextRow(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:]
	el := newRemainingServices(deps, f)
	// One full segment after the first hold: window shows ordinal "3rd".
	rendertest.AssertGolden(t, "testdata", "el_remaining_hold3rd", fbFor(t, el, 6+131))
}

func TestRemainingServicesWrapsSeamlessly(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:] // n = 4
	el := newRemainingServices(deps, f)
	// Hold frame of cycle 0 row 0 must equal hold frame of cycle 1 row 0.
	a := fbFor(t, el, 6)
	b := fbFor(t, el, 6+4*131)
	if string(a.Pix) != string(b.Pix) {
		t.Fatal("carousel wrap must be seamless")
	}
}
