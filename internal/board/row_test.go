package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func TestOrdinal(t *testing.T) {
	cases := map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 103: "103rd"}
	for n, want := range cases {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}

func renderRow(t *testing.T, d data.Departure, order int) *render.Framebuffer {
	t.Helper()
	fb := render.New(W, RowH)
	scene := &render.Scene{Elements: rowElements(d, order, 0, mustFonts(t))}
	scene.Render(fb, 0, fixedNow)
	return fb
}

func TestRowGoldenOnTime(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "row_ontime", renderRow(t, fixtureBoard().Departures[0], 1))
}

func TestRowGoldenExpected(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "row_expected", renderRow(t, fixtureBoard().Departures[1], 2))
}

func TestRowGoldenMissingPlatform(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "row_noplatform", renderRow(t, fixtureBoard().Departures[2], 3))
}

func TestRowGoldenCancelled(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "row_cancelled", renderRow(t, fixtureBoard().Departures[4], 5))
}

// A row with no platform must leave the platform box pixels untouched.
func TestRowNoPlatformLeavesGap(t *testing.T) {
	fb := renderRow(t, fixtureBoard().Departures[2], 3)
	for x := ColPlatformX; x < ColPlatformX+ColPlatformW; x++ {
		for y := 0; y < RowH; y++ {
			if fb.At(x, y) != 0 {
				t.Fatalf("platform box pixel (%d,%d) = %d, want 0", x, y, fb.At(x, y))
			}
		}
	}
}
