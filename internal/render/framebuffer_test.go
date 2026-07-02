package render

import (
	"bytes"
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
