package render

import (
	"time"
)

var _ Element = (*Clock)(nil)

// Clock renders HH:MM in a large font with :SS in a tall font beside it,
// centered horizontally. Mirrors the reference board's clock layout.
type Clock struct {
	Large *Font // HH:MM, ~20px
	Tall  *Font // :SS, ~10px
	W     int
	Level byte
}

const clockSecondsDrop = 5 // reference offset: seconds sit 5px lower

// Render draws the clock for the given time.
func (c *Clock) Render(fb *Framebuffer, _ int, now time.Time) {
	hourmin := now.Format("15:04")
	seconds := now.Format(":05")
	w1, _ := c.Large.Measure(hourmin)
	w2, _ := c.Tall.Measure(seconds)
	margin := alignX(AlignCenter, c.W, w1+w2)
	fb.BlitAlpha(c.Large.RenderText(hourmin), margin, 0, c.Level)
	fb.BlitAlpha(c.Tall.RenderText(seconds), margin+w1, clockSecondsDrop, c.Level)
}
