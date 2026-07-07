package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/render"
)

func TestPreviewSinkWritesDecodablePNG(t *testing.T) {
	dir := t.TempDir()
	s := newPreviewSink(dir, 1) // every flush
	fb := render.New(256, 64)
	fb.SetPixel(0, 0, 15)   // top-left: high nibble of byte 0
	fb.SetPixel(255, 63, 8) // bottom-right: low nibble of last byte
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "frame.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 64 {
		t.Fatalf("bounds = %v", img.Bounds())
	}
	r, _, _, _ := img.At(0, 0).RGBA()
	if r>>8 != 255 { // level 15 × 17
		t.Fatalf("pixel (0,0) = %d, want 255 — nibble order wrong?", r>>8)
	}
	r, _, _, _ = img.At(255, 63).RGBA()
	if r>>8 != 8*17 {
		t.Fatalf("pixel (255,63) = %d, want %d", r>>8, 8*17)
	}
}

func TestPreviewSinkRateLimits(t *testing.T) {
	dir := t.TempDir()
	s := newPreviewSink(dir, 25)
	fb := render.New(256, 64)
	for i := 0; i < 24; i++ {
		if err := s.Flush(fb.Pack()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "frame.png")); !os.IsNotExist(err) {
		t.Fatal("no PNG expected before the 25th flush")
	}
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "frame.png")); err != nil {
		t.Fatal("PNG expected on the 25th flush")
	}
}

func TestPreviewSinkRecordsContrast(t *testing.T) {
	s := newPreviewSink(t.TempDir(), 1)
	if err := s.SetContrast(32); err != nil {
		t.Fatal(err)
	}
	if s.lastContrast != 32 {
		t.Fatalf("lastContrast = %d", s.lastContrast)
	}
}

func TestPreviewSinkLatestNilBeforeFirstEncode(t *testing.T) {
	s := newPreviewSink(t.TempDir(), 25)
	if got := s.Latest(); got != nil {
		t.Fatalf("Latest() = %v, want nil before any encoding flush", got)
	}
}

func TestPreviewSinkLatestValidPNGAfterEncode(t *testing.T) {
	s := newPreviewSink(t.TempDir(), 1) // every flush encodes
	fb := render.New(256, 64)
	fb.SetPixel(0, 0, 15)
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	got := s.Latest()
	if got == nil {
		t.Fatal("Latest() = nil, want a PNG after an encoding flush")
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("Latest() did not decode as PNG: %v", err)
	}
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 64 {
		t.Fatalf("bounds = %v", img.Bounds())
	}
}

func TestPreviewSinkLatestUpdatesOnSubsequentEncode(t *testing.T) {
	s := newPreviewSink(t.TempDir(), 1)
	fb := render.New(256, 64)
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	first := s.Latest()

	fb.SetPixel(10, 10, 15) // change one pixel
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	second := s.Latest()

	if bytes.Equal(first, second) {
		t.Fatal("Latest() unchanged after a subsequent encode with a different frame")
	}
}

// TestPreviewSinkLatestSnapshotIsImmutable pins the Task 7 review's binding
// carry-over: /preview.png writes Latest()'s returned slice directly to the
// response without copying, so each new encoded frame MUST allocate a fresh
// []byte and swap the pointer rather than mutate/reuse the old buffer. A
// caller holding an earlier Latest() result must see it stay exactly as it
// was, no matter how many frames are flushed afterwards.
func TestPreviewSinkLatestSnapshotIsImmutable(t *testing.T) {
	s := newPreviewSink(t.TempDir(), 1)
	fb := render.New(256, 64)
	fb.SetPixel(0, 0, 15)
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	a := s.Latest()

	aImg, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatalf("a did not decode as PNG: %v", err)
	}
	r, _, _, _ := aImg.At(0, 0).RGBA()
	if r>>8 != 255 {
		t.Fatalf("a pixel (0,0) = %d, want 255 before the mutating flush", r>>8)
	}

	fb.SetPixel(0, 0, 0) // different frame: (0,0) now off
	fb.SetPixel(1, 0, 15)
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	b := s.Latest()

	if bytes.Equal(a, b) {
		t.Fatal("b should differ from a: the sink flushed a different frame")
	}

	// a must still decode to the FIRST frame's pixels — proving Latest()
	// returned an immutable snapshot, not a slice that got mutated in place.
	aAgain, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatalf("a decode after second flush: %v", err)
	}
	r, _, _, _ = aAgain.At(0, 0).RGBA()
	if r>>8 != 255 {
		t.Fatalf("a pixel (0,0) after second flush = %d, want 255 (a must not mutate)", r>>8)
	}
}

func TestPreviewSinkMemoryOnlyNeverTouchesDisk(t *testing.T) {
	// Redirect os.TempDir() (what os.CreateTemp("", ...) would fall back to
	// if the sink didn't special-case dir=="") at an empty scratch dir, so a
	// regression that starts writing "somewhere" instead of skipping the
	// disk entirely is still caught even though it wouldn't touch cwd.
	fallbackDir := t.TempDir()
	t.Setenv("TMPDIR", fallbackDir)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}

	s := newPreviewSink("", 25)
	fb := render.New(256, 64)
	for i := 0; i < 25; i++ {
		if err := s.Flush(fb.Pack()); err != nil {
			t.Fatal(err)
		}
	}
	if s.Latest() == nil {
		t.Fatal("Latest() = nil, want a PNG (memory-only mode still encodes)")
	}

	afterFallback, err := os.ReadDir(fallbackDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterFallback) != 0 {
		t.Fatalf("memory-only sink wrote to the OS temp dir: %v", afterFallback)
	}

	after, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("memory-only sink wrote to cwd: before=%v after=%v", before, after)
	}
}
