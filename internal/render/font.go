package render

import (
	_ "embed"
	"image"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// RegularTTF is the embedded Dot Matrix Regular font, used for standard rows.
//
//go:embed "fonts/Dot Matrix Regular.ttf"
var RegularTTF []byte

// BoldTTF is the embedded Dot Matrix Bold font, used for emphasised rows.
//
//go:embed "fonts/Dot Matrix Bold.ttf"
var BoldTTF []byte

// BoldTallTTF is the embedded Dot Matrix Bold Tall font, used for the
// large clock/header row.
//
//go:embed "fonts/Dot Matrix Bold Tall.ttf"
var BoldTallTTF []byte

// Font is a sfnt face rasterized at a fixed pixel size with no hinting.
type Font struct {
	face    font.Face
	ascent  int
	descent int
}

// LoadFont parses a TTF and builds a face at pxSize pixels (DPI 72 ⇒ px==pt).
func LoadFont(ttf []byte, pxSize float64) (*Font, error) {
	f, err := opentype.Parse(ttf)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    pxSize,
		DPI:     72,
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil, err
	}
	m := face.Metrics()
	return &Font{
		face:    face,
		ascent:  m.Ascent.Ceil(),
		descent: m.Descent.Ceil(),
	}, nil
}

// Measure returns the pixel advance width and the line height (ascent+descent).
func (f *Font) Measure(s string) (w, h int) {
	adv := font.MeasureString(f.face, s)
	return adv.Ceil(), f.ascent + f.descent
}

// RenderText rasterizes s into a left-aligned alpha bitmap (0=transparent,
// 255=full ink), with the baseline placed at ascent so glyphs fit top-down.
func (f *Font) RenderText(s string) *image.Alpha {
	w, h := f.Measure(s)
	if w == 0 {
		w = 1
	}
	dst := image.NewAlpha(image.Rect(0, 0, w, h))
	d := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(image.White), // white ⇒ alpha=255 ink
		Face: f.face,
		Dot:  fixed.P(0, f.ascent),
	}
	d.DrawString(s)
	return dst
}
