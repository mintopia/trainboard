package render

import "time"

// Align controls horizontal placement of text within an element's width.
type Align int

// Alignment options for horizontal text placement.
const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Element draws itself into the framebuffer for a given frame tick and time.
type Element interface {
	Render(fb *Framebuffer, tick int, now time.Time)
}

// Scene is an ordered list of elements composited back-to-front.
type Scene struct {
	Elements []Element
}

// Render draws every element in order.
func (s *Scene) Render(fb *Framebuffer, tick int, now time.Time) {
	for _, e := range s.Elements {
		e.Render(fb, tick, now)
	}
}

// alignX returns the x offset within a box of width w for content of width cw.
func alignX(a Align, w, cw int) int {
	switch a {
	case AlignRight:
		return w - cw
	case AlignCenter:
		return (w - cw) / 2
	default:
		return 0
	}
}
