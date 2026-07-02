// Package render provides a 4-bit greyscale framebuffer, sfnt font
// rasterization, and a scene/element engine for the departure board.
package render

import "image"

// Framebuffer is a W×H grid of 4-bit greyscale pixels (levels 0–15), one
// byte per pixel for cheap drawing. Pack() converts to SSD1322 wire format.
type Framebuffer struct {
	W, H int
	Pix  []byte
}

// New returns a cleared W×H framebuffer.
func New(w, h int) *Framebuffer {
	return &Framebuffer{W: w, H: h, Pix: make([]byte, w*h)}
}

// Clear resets every pixel to 0 (black).
func (fb *Framebuffer) Clear() {
	for i := range fb.Pix {
		fb.Pix[i] = 0
	}
}

// SetPixel writes level (clamped 0–15) at (x,y); out-of-bounds is ignored.
func (fb *Framebuffer) SetPixel(x, y int, level byte) {
	if x < 0 || y < 0 || x >= fb.W || y >= fb.H {
		return
	}
	if level > 0x0F {
		level = 0x0F
	}
	fb.Pix[y*fb.W+x] = level
}

// At returns the level at (x,y), or 0 if out of bounds.
func (fb *Framebuffer) At(x, y int) byte {
	if x < 0 || y < 0 || x >= fb.W || y >= fb.H {
		return 0
	}
	return fb.Pix[y*fb.W+x]
}

// Pack encodes the framebuffer into SSD1322 4-bit format: two horizontally
// adjacent pixels per byte, high nibble = left pixel. Length is W*H/2.
//
// Pack assumes W is even (256 is, for the target display). If a future
// partial buffer has odd width, add a guard — YAGNI for now.
func (fb *Framebuffer) Pack() []byte {
	out := make([]byte, fb.W*fb.H/2)
	oi := 0
	for y := 0; y < fb.H; y++ {
		row := y * fb.W
		for x := 0; x < fb.W; x += 2 {
			hi := fb.Pix[row+x] & 0x0F
			lo := fb.Pix[row+x+1] & 0x0F
			out[oi] = hi<<4 | lo
			oi++
		}
	}
	return out
}

// BlitAlpha composites an alpha bitmap at (x,y): each source coverage value
// (0–255) becomes level round(alpha*level/255), overwriting the source
// rectangle. Clipped to the framebuffer bounds.
func (fb *Framebuffer) BlitAlpha(src *image.Alpha, x, y int, level byte) {
	b := src.Bounds()
	for sy := b.Min.Y; sy < b.Max.Y; sy++ {
		for sx := b.Min.X; sx < b.Max.X; sx++ {
			a := int(src.AlphaAt(sx, sy).A)
			v := (a*int(level) + 127) / 255 // round to nearest
			fb.SetPixel(x+(sx-b.Min.X), y+(sy-b.Min.Y), byte(v))
		}
	}
}
