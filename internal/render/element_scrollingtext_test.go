package render

import (
	"testing"
	"time"
)

func TestScrollingShortTextStaticGolden(t *testing.T) {
	fb := New(256, 12)
	el := &ScrollingText{Font: mustFont(t, RegularTTF, 10), Text: "On time",
		X: 0, Y: 0, W: 256, H: 12, Level: 15}
	el.Render(fb, 0, time.Time{})
	assertGolden(t, "scroll_short_t0", fb)
}

// TestScrollOffsetPauseThenScroll covers the new (tw, boxW, pause, tick)
// signature: 0 for the whole pause window, then 1px/tick afterwards.
func TestScrollOffsetPauseThenScroll(t *testing.T) {
	const tw, boxW, pause = 400, 256, 5

	if off := scrollOffset(tw, boxW, pause, 0); off != 0 {
		t.Fatalf("tick 0: expected 0 offset during pause, got %d", off)
	}
	if off := scrollOffset(tw, boxW, pause, pause-1); off != 0 {
		t.Fatalf("tick %d: expected 0 offset during pause, got %d", pause-1, off)
	}
	off0 := scrollOffset(tw, boxW, pause, pause)   // first moving tick
	off1 := scrollOffset(tw, boxW, pause, pause+1) // next moving tick
	if off0 != 0 {
		t.Fatalf("first moving tick should start at offset 0, got %d", off0)
	}
	if off1 != off0+1 {
		t.Fatalf("expected 1px/tick after pause: off0=%d off1=%d", off0, off1)
	}
}

// TestScrollOffsetFitsAlwaysZero: text no wider than the box never scrolls.
func TestScrollOffsetFitsAlwaysZero(t *testing.T) {
	for _, tick := range []int{0, 1, 60, 1000} {
		if off := scrollOffset(100, 256, 5, tick); off != 0 {
			t.Fatalf("tick %d: text fits box, expected 0, got %d", tick, off)
		}
	}
}

// TestScrollOffsetDefaultPause exercises the "if pause <= 0 { pause =
// defaultPauseTicks }" branch.
func TestScrollOffsetDefaultPause(t *testing.T) {
	const tw, boxW = 400, 256

	if off := scrollOffset(tw, boxW, 0, defaultPauseTicks-1); off != 0 {
		t.Fatalf("tick %d: expected 0 (still within default pause), got %d", defaultPauseTicks-1, off)
	}
	if off := scrollOffset(tw, boxW, 0, defaultPauseTicks); off != 0 {
		t.Fatalf("tick %d: expected 0 (first post-pause tick), got %d", defaultPauseTicks, off)
	}
	if off := scrollOffset(tw, boxW, 0, defaultPauseTicks+1); off != 1 {
		t.Fatalf("tick %d: expected 1 (1px/tick after pause), got %d", defaultPauseTicks+1, off)
	}
}

// TestScrollOffsetWraps: after the text fully scrolls out (offset tw) the
// offset HOLDS there for a full end pause — blank, matching the reference's
// 2s pause before reset — and only then wraps to a fresh start pause.
func TestScrollOffsetWraps(t *testing.T) {
	const tw, boxW, pause = 20, 10, 3
	cycle := pause + tw + pause // start pause + travel + end pause

	if off := scrollOffset(tw, boxW, pause, pause+tw-1); off != tw-1 {
		t.Fatalf("last visible tick: expected %d, got %d", tw-1, off)
	}
	if off := scrollOffset(tw, boxW, pause, pause+tw); off != tw {
		t.Fatalf("first blank tick: expected offset %d, got %d", tw, off)
	}
	if off := scrollOffset(tw, boxW, pause, pause+tw+pause-1); off != tw {
		t.Fatalf("end pause must hold blank: expected %d, got %d", tw, off)
	}
	if off := scrollOffset(tw, boxW, pause, cycle); off != 0 {
		t.Fatalf("wrapped tick: expected fresh pause (0), got %d", off)
	}
	if off := scrollOffset(tw, boxW, pause, cycle+pause-1); off != 0 {
		t.Fatalf("still within wrapped pause: expected 0, got %d", off)
	}
}

// TestScrollingTextVisibleDuringPause: at tick 0 the text is left-aligned
// beside its label (visible immediately, no enter-from-right), and nothing
// is drawn outside the box.
func TestScrollingTextVisibleDuringPause(t *testing.T) {
	const boxX, boxW = 42, 214
	fb := New(256, 12)
	el := &ScrollingText{
		Font: mustFont(t, RegularTTF, 10),
		Text: "This train is formed of 8 coaches. Please mind the gap between the train and the platform edge.",
		X:    boxX, Y: 0, W: boxW, H: 12, Level: 15,
	}
	el.Render(fb, 0, time.Time{})

	inkInLeftEdge := false
	for y := 0; y < fb.H; y++ {
		for x := boxX; x < boxX+18 && x < boxX+boxW; x++ {
			if fb.At(x, y) != 0 {
				inkInLeftEdge = true
			}
		}
	}
	if !inkInLeftEdge {
		t.Fatalf("expected ink in [%d,%d) at tick 0 (visible from start, no enter-from-right)", boxX, boxX+18)
	}
	for y := 0; y < fb.H; y++ {
		for x := 0; x < fb.W; x++ {
			if x >= boxX && x < boxX+boxW {
				continue
			}
			if fb.At(x, y) != 0 {
				t.Fatalf("ink at (%d,%d) outside box [%d,%d)", x, y, boxX, boxX+boxW)
			}
		}
	}
}

// TestScrollingTextClipsToBox: scrolled text never draws outside its box,
// at a mid-scroll tick and a near-exit tick.
func TestScrollingTextClipsToBox(t *testing.T) {
	const boxX, boxW, pause = 50, 60, 5
	f := mustFont(t, RegularTTF, 10)
	text := "This train is formed of 8 coaches. Please mind the gap between the train and the platform edge."
	tw, _ := f.Measure(text)

	checkNoInkOutside := func(t *testing.T, tick int) {
		t.Helper()
		fb := New(256, 12)
		el := &ScrollingText{Font: f, Text: text, X: boxX, Y: 0, W: boxW, H: 12, Level: 15, PauseTicks: pause}
		el.Render(fb, tick, time.Time{})
		for y := 0; y < fb.H; y++ {
			for x := 0; x < fb.W; x++ {
				if x >= boxX && x < boxX+boxW {
					continue
				}
				if fb.At(x, y) != 0 {
					t.Fatalf("tick %d: ink at (%d,%d) outside box [%d,%d)", tick, x, y, boxX, boxX+boxW)
				}
			}
		}
	}

	midScrollTick := pause + tw/2
	nearExitTick := pause + tw - 2

	t.Run("mid_scroll", func(t *testing.T) { checkNoInkOutside(t, midScrollTick) })
	t.Run("near_exit", func(t *testing.T) { checkNoInkOutside(t, nearExitTick) })
}

// TestScrollingTextScrollsOutAndResets: once the text has fully scrolled out
// it stays blank for a whole end pause (reference parity: pause after
// finishing the scroll, before resetting), then the wrapped tick is back
// left-aligned, identical to tick 0.
func TestScrollingTextScrollsOutAndResets(t *testing.T) {
	const boxX, boxW, pause = 42, 214, 5
	f := mustFont(t, RegularTTF, 10)
	text := "This train is formed of 8 coaches. Please mind the gap between the train and the platform edge."
	tw, _ := f.Measure(text)

	newEl := func() *ScrollingText {
		return &ScrollingText{Font: f, Text: text, X: boxX, Y: 0, W: boxW, H: 12, Level: 15, PauseTicks: pause}
	}

	// Blank throughout the end pause: first blank tick and last blank tick.
	for _, blankTick := range []int{pause + tw, pause + tw + pause - 1} {
		fbBlank := New(256, 12)
		newEl().Render(fbBlank, blankTick, time.Time{})
		for i, v := range fbBlank.Pix {
			if v != 0 {
				t.Fatalf("end-pause tick %d: expected nothing drawn, pixel %d = %d", blankTick, i, v)
			}
		}
	}

	fb0 := New(256, 12)
	newEl().Render(fb0, 0, time.Time{})
	fbWrapped := New(256, 12)
	newEl().Render(fbWrapped, pause+tw+pause, time.Time{}) // cycle start
	for i := range fb0.Pix {
		if fb0.Pix[i] != fbWrapped.Pix[i] {
			t.Fatalf("wrapped tick %d differs from tick 0 at pixel %d: %d != %d", pause+tw+pause, i, fbWrapped.Pix[i], fb0.Pix[i])
		}
	}
}

// TestScrollingTextCachesRasterization: Font.RenderText is only called once
// per element, regardless of how many times Render is called (ADR 0002).
func TestScrollingTextCachesRasterization(t *testing.T) {
	fb := New(256, 12)
	el := &ScrollingText{Font: mustFont(t, RegularTTF, 10), Text: "On time",
		X: 0, Y: 0, W: 256, H: 12, Level: 15}
	el.Render(fb, 0, time.Time{})
	first := el.img
	if first == nil {
		t.Fatal("expected img to be cached after first Render")
	}
	el.Render(fb, 1, time.Time{})
	if el.img != first {
		t.Fatalf("expected s.img pointer unchanged between renders, got a new rasterization")
	}
}

func TestScrollingLongTextGoldenMidScroll(t *testing.T) {
	fb := New(256, 12)
	el := &ScrollingText{Font: mustFont(t, RegularTTF, 10),
		Text: "This train is formed of 8 coaches. Please mind the gap between the train and the platform edge.",
		X: 0, Y: 0, W: 256, H: 12, Level: 15, PauseTicks: 5}
	el.Render(fb, 40, time.Time{})
	assertGolden(t, "scroll_long_t40", fb)
}
