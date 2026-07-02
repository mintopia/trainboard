package render

import "time"

const defaultPauseTicks = 60 // reference paused 60 frames before scrolling

var _ Element = (*ScrollingText)(nil)

// ScrollingText renders one line inside a box. If the text is wider than the
// box it scrolls right-to-left at one pixel per tick after an initial pause;
// otherwise it is left-aligned and static.
type ScrollingText struct {
	Font       *Font
	Text       string
	X, Y, W, H int
	Level      byte
	PauseTicks int // 0 ⇒ defaultPauseTicks
}

// scrollOffset is the pure integer-pixel scroll offset for a given tick.
// Returns 0 while the text fits the box. During [0,pause) the offset is 0;
// after that it advances one pixel per tick, wrapping so the scroll loops.
func scrollOffset(f *Font, text string, boxW, pause, tick int) int {
	tw, _ := f.Measure(text)
	if tw <= boxW {
		return 0
	}
	if pause <= 0 {
		pause = defaultPauseTicks
	}
	if tick < pause {
		return 0
	}
	// Total travel: text scrolls until it has fully passed, then repeats.
	travel := tw + boxW
	return (tick - pause) % travel
}

// Render composites the (possibly scrolled) text into fb.
func (s *ScrollingText) Render(fb *Framebuffer, tick int, _ time.Time) {
	if s.Text == "" {
		return
	}
	img := s.Font.RenderText(s.Text)
	tw := img.Bounds().Dx()
	if tw <= s.W {
		fb.BlitAlpha(img, s.X, s.Y, s.Level) // left-aligned, static
		return
	}
	off := scrollOffset(s.Font, s.Text, s.W, s.PauseTicks, tick)
	// Draw so the text enters from the right and exits left: start at box
	// right edge, move left by off.
	fb.BlitAlpha(img, s.X+s.W-off, s.Y, s.Level)
}
