package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/render"
)

func TestLoadConfigMissingFileFailsValidation(t *testing.T) {
	// config.Load returns Default(), nil for a missing file — Default() has an
	// empty Board.Origin/Darwin.Token, so loadConfig must reject it via
	// Validate rather than let a fresh install run with empty values.
	_, err := loadConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected error for missing config file (Default() must fail Validate)")
	}
}

func TestLoadConfigInvalidValuesFailValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
		"version": 1,
		"darwin": {"token": "tok123"},
		"board": {
			"origin": "PAD",
			"services": 3,
			"cutoffHours": 8,
			"refreshSeconds": 5,
			"timeWindowMinutes": 120
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected error for refreshSeconds below the 15s minimum")
	}
}

func TestLoadConfigValidFileReturnsMatchingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
		"version": 1,
		"darwin": {"token": "tok123"},
		"board": {
			"origin": "PAD",
			"services": 3,
			"cutoffHours": 8,
			"refreshSeconds": 60,
			"timeWindowMinutes": 120
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want := config.Config{
		Version: 1,
		Darwin:  config.DarwinConfig{Token: "tok123"},
		Board: config.BoardConfig{
			Origin:            "PAD",
			Services:          3,
			CutoffHours:       8,
			RefreshSeconds:    60,
			TimeWindowMinutes: 120,
		},
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadConfig = %+v, want %+v", got, want)
	}
}

// TestTeeFlusherWithPreviewSinkComposition exercises the production wiring
// end to end: teeFlusher(panel, previewSink) with the sink's dir=="" (no
// disk writes, matching newPreviewSink("", 25) teed with the panel in run())
// still keeps sink.Latest() — the exact func wired into web.Sources.PreviewPNG
// — serving a decodable, up-to-date frame after every render-loop Flush.
func TestTeeFlusherWithPreviewSinkComposition(t *testing.T) {
	panel := &fakeFlusher{}
	sink := newPreviewSink("", 1) // every flush encodes; memory-only
	tee := newTeeFlusher(panel, sink, testLog())

	fb := render.New(256, 64)
	fb.SetPixel(0, 0, 15)
	if err := tee.Flush(fb.Pack()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if panel.flushCalls != 1 {
		t.Fatalf("panel.flushCalls = %d, want 1", panel.flushCalls)
	}

	latest := sink.Latest()
	if latest == nil {
		t.Fatal("sink.Latest() = nil after a Flush through the tee")
	}
	img, err := png.Decode(bytes.NewReader(latest))
	if err != nil {
		t.Fatalf("Latest() did not decode as PNG: %v", err)
	}
	r, _, _, _ := img.At(0, 0).RGBA()
	if r>>8 != 255 {
		t.Fatalf("pixel (0,0) = %d, want 255", r>>8)
	}

	if err := tee.SetContrast(20); err != nil {
		t.Fatalf("SetContrast: %v", err)
	}
	if panel.contrastCalls != 1 || panel.lastContrast != 20 {
		t.Fatalf("panel contrast calls=%d last=%d, want 1/20", panel.contrastCalls, panel.lastContrast)
	}
}
