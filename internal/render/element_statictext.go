package render

import "time"

var _ Element = (*StaticText)(nil)

// StaticText renders a single line of text within a fixed box, horizontally
// aligned and top-anchored.
type StaticText struct {
	Font       *Font
	Text       string
	X, Y, W, H int
	Align      Align
	Level      byte
}

// Render composites the text into fb (tick/now unused — static content).
func (s *StaticText) Render(fb *Framebuffer, _ int, _ time.Time) {
	if s.Text == "" {
		return
	}
	cw, _ := s.Font.Measure(s.Text)
	dx := alignX(s.Align, s.W, cw)
	fb.BlitAlpha(s.Font.RenderText(s.Text), s.X+dx, s.Y, s.Level)
}
