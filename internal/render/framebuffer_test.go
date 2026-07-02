package render

import (
	"bytes"
	"image"
	"testing"
)

func TestPackNibbleOrder(t *testing.T) {
	fb := New(4, 1)
	fb.SetPixel(0, 0, 0x0A) // left pixel of byte 0
	fb.SetPixel(1, 0, 0x0B) // right pixel of byte 0
	fb.SetPixel(2, 0, 0x0C)
	fb.SetPixel(3, 0, 0x0D)
	got := fb.Pack()
	want := []byte{0xAB, 0xCD}
	if !bytes.Equal(got, want) {
		t.Fatalf("Pack() = % X, want % X", got, want)
	}
}

func TestPackFullFrameLength(t *testing.T) {
	fb := New(256, 64)
	if got := len(fb.Pack()); got != 8192 {
		t.Fatalf("full-frame Pack len = %d, want 8192", got)
	}
}

func TestSetPixelClampsAndClips(t *testing.T) {
	fb := New(2, 1)
	fb.SetPixel(0, 0, 0xFF) // clamp to 0x0F
	fb.SetPixel(99, 0, 5)   // out of bounds: no panic, ignored
	if fb.At(0, 0) != 0x0F {
		t.Fatalf("level not clamped: got %#x", fb.At(0, 0))
	}
}

func TestClear(t *testing.T) {
	fb := New(2, 2)
	fb.SetPixel(0, 0, 9)
	fb.Clear()
	if fb.At(0, 0) != 0 {
		t.Fatalf("Clear did not zero pixel")
	}
}

func TestBlitAlphaScalesByLevel(t *testing.T) {
	fb := New(2, 1)
	src := image.NewAlpha(image.Rect(0, 0, 2, 1))
	src.Pix[0] = 255 // full ink
	src.Pix[1] = 0   // transparent
	fb.BlitAlpha(src, 0, 0, 15)
	if fb.At(0, 0) != 15 {
		t.Fatalf("full ink at level 15 = %d, want 15", fb.At(0, 0))
	}
	if fb.At(1, 0) != 0 {
		t.Fatalf("transparent px = %d, want 0", fb.At(1, 0))
	}
}

func TestBlitAlphaMidLevel(t *testing.T) {
	fb := New(1, 1)
	src := image.NewAlpha(image.Rect(0, 0, 1, 1))
	src.Pix[0] = 128 // ~half
	fb.BlitAlpha(src, 0, 0, 15)
	// round(128*15/255) = round(7.53) = 8
	if fb.At(0, 0) != 8 {
		t.Fatalf("mid ink = %d, want 8", fb.At(0, 0))
	}
}

func TestBlitAlphaClips(t *testing.T) {
	fb := New(2, 2)
	src := image.NewAlpha(image.Rect(0, 0, 2, 2))
	for i := range src.Pix {
		src.Pix[i] = 255
	}
	fb.BlitAlpha(src, 1, 1, 15) // only (1,1) lands
	if fb.At(1, 1) != 15 || fb.At(0, 0) != 0 {
		t.Fatalf("clip failed: (1,1)=%d (0,0)=%d", fb.At(1, 1), fb.At(0, 0))
	}
}
