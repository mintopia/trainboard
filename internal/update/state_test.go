package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updater", "state.json")
	want := State{
		Active: "b", ActiveVersion: "v0.2.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 2, VersionFloor: "v0.1.0", RolledBackFrom: "v0.1.9",
	}
	if err := SaveState(path, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

func TestLoadStateMissingIsErrNotExist(t *testing.T) {
	_, err := LoadState(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing state: got %v, want fs.ErrNotExist", err)
	}
}

func TestLoadStateRejectsBadSlotNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"active":"c","known_good":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Error("bad active slot name accepted")
	}
}

func TestLoadStateOrDefaultDegrades(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing": filepath.Join(dir, "missing.json"),
		"corrupt": corrupt,
	} {
		if got := LoadStateOrDefault(path); got != DefaultState() {
			t.Errorf("%s: got %+v, want DefaultState", name, got)
		}
	}
}

func TestDefaultState(t *testing.T) {
	d := DefaultState()
	if d.Active != "a" || d.KnownGood != "a" || d.BootAttempts != 0 {
		t.Errorf("DefaultState = %+v", d)
	}
}
