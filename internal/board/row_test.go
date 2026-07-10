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
		if got := Ordinal(n); got != want {
			t.Errorf("Ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}

func renderRow(t *testing.T, d data.Departure, order int) *render.Framebuffer {
	t.Helper()
	return renderRowHC(t, d, order, false)
}

func renderRowHC(t *testing.T, d data.Departure, order int, headcodes bool) *render.Framebuffer {
	t.Helper()
	fb := render.New(W, RowH)
	scene := &render.Scene{Elements: rowElements(d, order, 0, mustFonts(t), headcodes)}
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

func TestRowGoldenHeadcode(t *testing.T) {
	d := fixtureBoard().Departures[0]
	d.Headcode = "1A23"
	rendertest.AssertGolden(t, "testdata", "row_headcode", renderRowHC(t, d, 1, true))
}

// Headcodes ON but this service unmatched: the column stays a gap and the
// platform/destination still shift — column positions must not depend on
// per-row data.
func TestRowGoldenHeadcodeBlank(t *testing.T) {
	d := fixtureBoard().Departures[0]
	d.Headcode = ""
	rendertest.AssertGolden(t, "testdata", "row_headcode_blank", renderRowHC(t, d, 1, true))
}

// Flag off: pixel-identical to the pre-feature renderer even when the data
// carries a headcode (existing goldens already lock the geometry; this locks
// the "ignores the field" contract).
func TestRowHeadcodeOffIgnoresField(t *testing.T) {
	d := fixtureBoard().Departures[0]
	plain := renderRowHC(t, d, 1, false)
	d.Headcode = "1A23"
	withField := renderRowHC(t, d, 1, false)
	if string(plain.Pix) != string(withField.Pix) {
		t.Fatal("headcodes-off row must ignore the Headcode field")
	}
}

// With headcodes on, the headcode box [45,72) and shifted platform box
// [72,91) must hold the right content: blank headcode leaves [45,72) dark.
func TestRowHeadcodeBlankLeavesGap(t *testing.T) {
	d := fixtureBoard().Departures[0]
	d.Headcode = ""
	fb := renderRowHC(t, d, 1, true)
	for x := ColHeadcodeX; x < ColHeadcodeX+ColHeadcodeW; x++ {
		for y := 0; y < RowH; y++ {
			if fb.At(x, y) != 0 {
				t.Fatalf("headcode box pixel (%d,%d) = %d, want 0", x, y, fb.At(x, y))
			}
		}
	}
}
