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
	el := newNextServiceRow(fixtureBoard().Departures[0], f, false)
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
	el := newNextServiceRow(fixtureBoard().Departures[0], f, false)
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
	fb := fbFor(t, newRemainingServices(nil, f, false), 100)
	for i, p := range fb.Pix {
		if p != 0 {
			t.Fatalf("pixel %d lit for empty remaining services", i)
		}
	}
}

func TestRemainingServicesSlidesFirstRowIn(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:] // ordinals 2..5
	el := newRemainingServices(deps, f, false)

	// t=5: end of the blank scroll-in phase, band still fully blank.
	at5 := fbFor(t, el, 5)
	for y := RemainingY; y < RemainingY+RowH; y++ {
		for x := 0; x < W; x++ {
			if at5.At(x, y) != 0 {
				t.Fatalf("pixel (%d,%d) lit at t=5, band should still be blank", x, y)
			}
		}
	}

	// t=8: mid first move (top=6), row 1 is sliding in so the band has some
	// lit pixels but isn't yet the fully-held frame.
	at8 := fbFor(t, el, 8)
	lit := false
	for y := RemainingY; y < RemainingY+RowH && !lit; y++ {
		for x := 0; x < W; x++ {
			if at8.At(x, y) != 0 {
				lit = true
				break
			}
		}
	}
	if !lit {
		t.Fatal("expected some lit pixels at t=8 (row 1 sliding in)")
	}

	at11 := fbFor(t, el, 11)
	if string(at8.Pix) == string(at5.Pix) {
		t.Fatal("t=8 frame must differ from t=5 (blank) frame")
	}
	if string(at8.Pix) == string(at11.Pix) {
		t.Fatal("t=8 frame must differ from t=11 (fully-held) frame")
	}
}

func TestRemainingServicesHoldsRowDuringPause(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:] // ordinals 2..5
	el := newRemainingServices(deps, f, false)
	// Row 1 finishes sliding in at t=11 (top=12) and holds until t=136
	// (t'=5..130, i.e. rsPauseTicks worth of held frames).
	a := fbFor(t, el, 11)
	b := fbFor(t, el, 136)
	if string(a.Pix) != string(b.Pix) {
		t.Fatal("carousel must hold the row for the whole pause")
	}
	rendertest.AssertGolden(t, "testdata", "el_remaining_hold2nd", a)
}

func TestRemainingServicesAdvancesToNextRow(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:]
	el := newRemainingServices(deps, f, false)
	// One full segment after the first hold: window shows ordinal "3rd".
	rendertest.AssertGolden(t, "testdata", "el_remaining_hold3rd", fbFor(t, el, 11+131))
}

func TestRemainingServicesWrapsSeamlessly(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:] // n = 4
	el := newRemainingServices(deps, f, false)
	// Hold frame of cycle 0 row 0 must equal hold frame of cycle 1 row 0.
	a := fbFor(t, el, 11)
	b := fbFor(t, el, 11+4*131)
	if string(a.Pix) != string(b.Pix) {
		t.Fatal("carousel wrap must be seamless")
	}
}
