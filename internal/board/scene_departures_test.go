package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func TestCallingAtText(t *testing.T) {
	d := fixtureBoard().Departures[0] // Reading, Didcot Parkway, Swindon
	if got, want := callingAtText(d, false), "Reading, Didcot Parkway and Swindon"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := callingAtText(d, true), "Reading (11:00), Didcot Parkway (11:01) and Swindon (11:02)"; got != want {
		t.Errorf("with times: got %q, want %q", got, want)
	}
	one := fixtureBoard().Departures[3] // single stop: Reading
	if got, want := callingAtText(one, false), "Reading"; got != want {
		t.Errorf("single: got %q, want %q", got, want)
	}
	none := fixtureBoard().Departures[4] // no calling points
	if got := callingAtText(none, false); got != "" {
		t.Errorf("empty: got %q, want \"\"", got)
	}
}

func TestServiceInfoText(t *testing.T) {
	d := fixtureBoard().Departures[0]
	if got, want := serviceInfoText(d), "Great Western Railway service formed of 5 coaches"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	d.Length = 1
	if got, want := serviceInfoText(d), "Great Western Railway service formed of 1 coach"; got != want {
		t.Errorf("singular: got %q, want %q", got, want)
	}
	d.Length = 0
	if got, want := serviceInfoText(d), "Great Western Railway service"; got != want {
		t.Errorf("no length: got %q, want %q", got, want)
	}
}

func sceneFrame(t *testing.T, tick int) *render.Framebuffer {
	t.Helper()
	s := departureBoardScene(fixtureBoard(), config.Default().Layout, mustFonts(t))
	fb := render.New(W, H)
	// Render every tick up to the target so stateless elements are exercised
	// exactly as the runtime loop would at that tick.
	fb.Clear()
	s.Render(fb, tick, fixedNow)
	return fb
}

func TestDepartureBoardGoldenSettled(t *testing.T) {
	// t=200: next service fully in, carousel holding "2nd", scrolls mid-cycle.
	rendertest.AssertGolden(t, "testdata", "scene_departures_t200", sceneFrame(t, 200))
}

func TestDepartureBoardGoldenFirstTick(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "scene_departures_t0", sceneFrame(t, 0))
}

func TestDepartureBoardSingleServiceLeavesCarouselBlank(t *testing.T) {
	s := departureBoardScene(singleDepBoard(), config.Default().Layout, mustFonts(t))
	fb := render.New(W, H)
	s.Render(fb, 200, fixedNow)
	for y := RemainingY; y < RemainingY+RowH; y++ {
		for x := 0; x < W; x++ {
			if fb.At(x, y) != 0 {
				t.Fatalf("carousel band pixel (%d,%d) lit with no remaining services", x, y)
			}
		}
	}
}
