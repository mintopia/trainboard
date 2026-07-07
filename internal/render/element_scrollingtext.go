package render

import (
	"image"
	"time"
)

const defaultPauseTicks = 60 // reference paused 60 frames before scrolling

var _ Element = (*ScrollingText)(nil)

// ScrollingText renders one line inside a box. If the text is wider than the
// box it is shown left-aligned during an initial pause, then scrolls left at
// one pixel per tick until it has fully exited the box, then wraps back to
// the left-aligned pause. Otherwise it is left-aligned and static.
type ScrollingText struct {
	Font       *Font
	Text       string
	X, Y, W, H int
	Level      byte
	PauseTicks int // 0 ⇒ defaultPauseTicks

	// img/tw cache the rasterization: elements are rebuilt on scene change,
	// so rasterize-once-per-element satisfies ADR 0002's "cached per text
	// change, not per frame".
	img *image.Alpha
	tw  int
}

// ensure lazily rasterizes s.Text into s.img/s.tw, once per element lifetime.
func (s *ScrollingText) ensure() {
	if s.img != nil {
		return
	}
	s.img = s.Font.RenderText(s.Text)
	s.tw = s.img.Bounds().Dx()
}

// scrollOffset is the pure integer-pixel viewport offset for a tick. Returns
// 0 while the text fits the box. Cycle: hold at 0 for pause ticks (text
// visible, left-aligned), advance 1px/tick until the text has fully scrolled
// out of the box (offset tw), hold blank there for another pause, then wrap
// back to the start — matching the reference's crop-window scroller, which
// pauses both at reset and after finishing the scroll.
func scrollOffset(tw, boxW, pause, tick int) int {
	if tw <= boxW {
		return 0
	}
	if pause <= 0 {
		pause = defaultPauseTicks
	}
	cycle := pause + tw + pause // start pause + travel + end pause
	t := tick % cycle
	if t < pause {
		return 0
	}
	if off := t - pause; off < tw {
		return off
	}
	return tw // end pause: fully scrolled out, hold blank before reset
}

// Render composites the (possibly scrolled) text into fb.
func (s *ScrollingText) Render(fb *Framebuffer, tick int, _ time.Time) {
	if s.Text == "" {
		return
	}
	s.ensure()
	if s.tw <= s.W {
		fb.BlitAlpha(s.img, s.X, s.Y, s.Level) // fits: static, left-aligned
		return
	}
	off := scrollOffset(s.tw, s.W, s.PauseTicks, tick)
	// Viewport crop [off, off+W) of the text, drawn at the box origin —
	// the text starts beside the label and exits left; nothing ever
	// draws outside the box (the crop IS the clip).
	srcX1 := off + s.W
	if srcX1 > s.tw {
		srcX1 = s.tw
	}
	if srcX1 <= off {
		return // fully scrolled out (the blank wrap tick)
	}
	sub, _ := s.img.SubImage(image.Rect(off, 0, srcX1, s.img.Bounds().Dy())).(*image.Alpha)
	fb.BlitAlpha(sub, s.X, s.Y, s.Level)
}
