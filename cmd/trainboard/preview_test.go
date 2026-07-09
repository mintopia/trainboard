package main

import (
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

// TestPreviewSinkEmptyDirNeverTouchesDisk pins previewSink's dir=="" guard:
// production no longer constructs a previewSink at all (Task 4 removed the
// web UI's PNG preview), but the guard itself stays as a defensive contract
// for any future zero-value/dir-less construction — Flush must be a safe
// no-op, not a panic or an attempt to write into the OS temp dir.
func TestPreviewSinkEmptyDirNeverTouchesDisk(t *testing.T) {
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

	afterFallback, err := os.ReadDir(fallbackDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterFallback) != 0 {
		t.Fatalf("dir==\"\" sink wrote to the OS temp dir: %v", afterFallback)
	}

	after, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("dir==\"\" sink wrote to cwd: before=%v after=%v", before, after)
	}
}
