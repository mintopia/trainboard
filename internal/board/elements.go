package board

import (
	"time"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
)

// Animation constants (ticks are 0.04s frames; reference parity).
const (
	nsStep       = 2   // next-service scroll-in px/tick
	rsStep       = 2   // remaining-services scroll px/tick
	rsPauseTicks = 125 // hold each row ~5s
	rsMoveTicks  = RowH / rsStep
	rsSegTicks   = rsPauseTicks + rsMoveTicks
)

// offset renders a child element into a scratch framebuffer and copies it to
// a fixed panel position. It lets position-less elements (render.Clock) and
// pre-rendered strips participate in absolute layout without touching render.
type offset struct {
	el      render.Element
	dx, dy  int
	scratch *render.Framebuffer
}

func offsetElement(el render.Element, dx, dy, w, h int) render.Element {
	return &offset{el: el, dx: dx, dy: dy, scratch: render.New(w, h)}
}

func (o *offset) Render(fb *render.Framebuffer, tick int, now time.Time) {
	o.scratch.Clear()
	o.el.Render(o.scratch, tick, now)
	copyRect(fb, o.scratch, 0, o.scratch.H, o.dx, o.dy)
}

// copyRect overwrites dst at (dx,dy) with src rows [srcY0, srcY1).
func copyRect(dst, src *render.Framebuffer, srcY0, srcY1, dx, dy int) {
	for y := srcY0; y < srcY1; y++ {
		ty := dy + y - srcY0
		if ty < 0 || ty >= dst.H {
			continue
		}
		for x := 0; x < src.W; x++ {
			tx := dx + x
			if tx < 0 || tx >= dst.W {
				continue
			}
			dst.SetPixel(tx, ty, src.At(x, y))
		}
	}
}

// prerender draws elements once into a fresh w×h framebuffer.
func prerender(els []render.Element, w, h int) *render.Framebuffer {
	fb := render.New(w, h)
	s := &render.Scene{Elements: els}
	s.Render(fb, 0, time.Time{})
	return fb
}

// nextServiceRow slides departure row 1 up from the bottom edge of its band
// (2px/tick, reference NextService), then holds it.
type nextServiceRow struct {
	strip *render.Framebuffer // 256×12 pre-rendered row
}

func newNextServiceRow(d data.Departure, f *Fonts, headcodes bool) render.Element {
	return &nextServiceRow{strip: prerender(rowElements(d, 1, 0, f, headcodes), W, RowH)}
}

func (n *nextServiceRow) Render(fb *render.Framebuffer, tick int, _ time.Time) {
	b := nsStep * (tick + 1)
	if b > RowH {
		b = RowH
	}
	copyRect(fb, n.strip, 0, b, 0, RowH-b)
}

// remainingServices vertically cycles rows 2..n (reference RemainingServices):
// each row moves into place then holds (move-then-hold, not hold-then-move) —
// scroll in, slide 12px into the next row, hold rsPauseTicks, repeat —
// wrapping seamlessly via a duplicated first row.
type remainingServices struct {
	strip *render.Framebuffer
	n     int
}

func newRemainingServices(deps []data.Departure, f *Fonts, headcodes bool) render.Element {
	if len(deps) == 0 {
		// n stays 0 (zero value); Render's nil-strip guard below depends on
		// strip being nil in this case, not on n.
		return &remainingServices{}
	}
	n := len(deps)
	var els []render.Element
	for i, d := range deps {
		els = append(els, rowElements(d, i+2, (i+1)*RowH, f, headcodes)...)
	}
	els = append(els, rowElements(deps[0], 2, (n+1)*RowH, f, headcodes)...)
	return &remainingServices{strip: prerender(els, W, (n+2)*RowH), n: n}
}

func (r *remainingServices) Render(fb *render.Framebuffer, tick int, _ time.Time) {
	if r.strip == nil {
		return
	}
	if tick < rsMoveTicks {
		// Scroll-in: strip rows [0,b) (blank row 0) at the band's bottom.
		b := rsStep * (tick + 1)
		copyRect(fb, r.strip, 0, b, 0, RemainingY+RowH-b)
		return
	}
	t := tick - rsMoveTicks
	s := t / rsSegTicks
	w := t % rsSegTicks
	step := rsStep * (w + 1)
	if step > RowH {
		step = RowH
	}
	u := RowH*s + step // continuous unwrapped scroll position
	top := u
	if u > RowH {
		top = RowH + (u-RowH)%(RowH*r.n) // wrap within real rows; dup row covers mid-move windows
	}
	copyRect(fb, r.strip, top, top+RowH, 0, RemainingY)
}
