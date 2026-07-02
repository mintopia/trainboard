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

func TestScrollingLongTextMovesLeft(t *testing.T) {
	long := "This train is formed of 8 coaches. Please mind the gap between the train and the platform edge."
	f := mustFont(t, RegularTTF, 10)
	pause := 5
	// During pause, offset is fixed; after pause it advances one px/tick.
	off0 := scrollOffset(f, long, 256, pause, pause)     // first moving frame
	off1 := scrollOffset(f, long, 256, pause, pause+1)   // next moving frame
	if off1 != off0+1 {
		t.Fatalf("expected 1px/tick after pause: off0=%d off1=%d", off0, off1)
	}
	if p := scrollOffset(f, long, 256, pause, 0); p != 0 {
		t.Fatalf("expected 0 offset during pause, got %d", p)
	}
}

func TestScrollOffsetDefaultPause(t *testing.T) {
	// Wide (402px) text so it doesn't short-circuit at tw<=boxW; PauseTicks=0
	// exercises the "if pause <= 0 { pause = defaultPauseTicks }" branch.
	long := "This train is formed of 8 coaches. Please mind the gap between the train and the platform edge."
	f := mustFont(t, RegularTTF, 10)
	const boxW = 256

	if off := scrollOffset(f, long, boxW, 0, defaultPauseTicks-1); off != 0 {
		t.Fatalf("tick %d: expected 0 (still within default pause), got %d", defaultPauseTicks-1, off)
	}
	if off := scrollOffset(f, long, boxW, 0, defaultPauseTicks); off != 0 {
		t.Fatalf("tick %d: expected 0 (first post-pause frame), got %d", defaultPauseTicks, off)
	}
	if off := scrollOffset(f, long, boxW, 0, defaultPauseTicks+1); off != 1 {
		t.Fatalf("tick %d: expected 1 (1px/tick after pause), got %d", defaultPauseTicks+1, off)
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
