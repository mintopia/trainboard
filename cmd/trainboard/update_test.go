package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/update"
)

// testLogSink is a discard-logger helper for update.go's tests; tee_test.go's
// testLog() has no *testing.T parameter, so it isn't a drop-in replacement
// for the t.Helper()-annotated signature used here.
func testLogSink(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildUpdaterDisabledWithoutStateFile(t *testing.T) {
	// Dev machine / pre-migration Pi: no state file ⇒ updater disabled but
	// non-nil (web shows "not available", nothing crashes).
	u := buildUpdater(config.Default(), t.TempDir(), filepath.Join(t.TempDir(), "absent.json"), testLogSink(t))
	if u == nil || u.enabled {
		t.Fatalf("updater = %+v, want non-nil disabled", u)
	}
	if st := u.checker.Status(); st.Enabled {
		t.Error("disabled updater reports Enabled status")
	}
}

func TestBuildUpdaterDisabledWithEmptyKeyring(t *testing.T) {
	// Slot install present but the key ceremony hasn't run: disabled.
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := update.SaveState(statePath, update.DefaultState()); err != nil {
		t.Fatal(err)
	}
	u := buildUpdater(config.Default(), t.TempDir(), statePath, testLogSink(t))
	// With embeddedKeys still empty this must be disabled; once the key
	// ceremony fills the keyring this branch flips — assert consistency
	// with update.Keyring() rather than a fixed expectation.
	if _, err := update.Keyring(); err != nil {
		if u.enabled {
			t.Error("updater enabled despite empty keyring")
		}
	} else if !u.enabled {
		t.Error("updater disabled despite state file + keyring")
	}
}

func TestProbeURLFromHTTPAddr(t *testing.T) {
	tests := []struct{ addr, want string }{
		{":80", "http://127.0.0.1:80/login"},
		{":8080", "http://127.0.0.1:8080/login"},
		{"0.0.0.0:8080", "http://127.0.0.1:8080/login"},
		{"192.168.0.5:80", "http://192.168.0.5:80/login"},
	}
	for _, tt := range tests {
		if got := probeURL(tt.addr); got != tt.want {
			t.Errorf("probeURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}
