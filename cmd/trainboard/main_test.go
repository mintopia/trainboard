package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mintopia/trainboard/internal/config"
)

// testLog is a *slog.Logger discarding output, for tests that don't assert
// on log content.
func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

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
