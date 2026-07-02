package render

import (
	"testing"
	"time"
)

func mustFont(t *testing.T, ttf []byte, px float64) *Font {
	t.Helper()
	f, err := LoadFont(ttf, px)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestStaticTextLeftGolden(t *testing.T) {
	fb := New(256, 12)
	el := &StaticText{Font: mustFont(t, RegularTTF, 10), Text: "London Paddington",
		X: 0, Y: 0, W: 256, H: 12, Align: AlignLeft, Level: 15}
	el.Render(fb, 0, time.Time{})
	assertGolden(t, "statictext_left", fb)
}

func TestStaticTextRightAlignsToRightEdge(t *testing.T) {
	fbL := New(256, 12)
	fbR := New(256, 12)
	txt := "1"
	(&StaticText{Font: mustFont(t, BoldTTF, 10), Text: txt, X: 0, Y: 0, W: 256, H: 12, Align: AlignLeft, Level: 15}).Render(fbL, 0, time.Time{})
	(&StaticText{Font: mustFont(t, BoldTTF, 10), Text: txt, X: 0, Y: 0, W: 256, H: 12, Align: AlignRight, Level: 15}).Render(fbR, 0, time.Time{})
	// Right-aligned ink must sit further right than left-aligned ink.
	leftmostInk := func(fb *Framebuffer) int {
		for x := 0; x < fb.W; x++ {
			for y := 0; y < fb.H; y++ {
				if fb.At(x, y) > 0 {
					return x
				}
			}
		}
		return fb.W
	}
	if leftmostInk(fbR) <= leftmostInk(fbL) {
		t.Fatalf("right-align did not shift ink right: L=%d R=%d", leftmostInk(fbL), leftmostInk(fbR))
	}
}
