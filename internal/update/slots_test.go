package update

import (
	"path/filepath"
	"testing"
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name string
		in   State
		want BootDecision
	}{
		{
			name: "fresh boot, attempts increment",
			in:   State{Active: "a", KnownGood: "a"},
			want: BootDecision{Slot: "a", State: State{Active: "a", KnownGood: "a", BootAttempts: 1}},
		},
		{
			name: "new slot within attempt budget",
			in:   State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 2},
			want: BootDecision{Slot: "b", State: State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 3}},
		},
		{
			name: "new slot exhausted attempts: rollback to known-good",
			in:   State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 3, VersionFloor: "v0.1.0"},
			want: BootDecision{Slot: "a", RolledBack: true, State: State{
				Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0",
				BootAttempts: 1, VersionFloor: "v0.1.0", RolledBackFrom: "v0.2.0",
			}},
		},
		{
			name: "known-good itself exhausted attempts: double fault, recovery",
			in:   State{Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 3},
			want: BootDecision{Slot: "a", Recovery: true, State: State{
				Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 0,
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.in); got != tt.want {
				t.Errorf("Decide(%+v)\n got %+v\nwant %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestPromote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// Pending update in slot b, one attempt burned, rollback marker from an
	// earlier bad release still showing.
	seed := State{
		Active: "b", ActiveVersion: "v0.2.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 1, RolledBackFrom: "v0.1.9",
	}
	if err := SaveState(path, seed); err != nil {
		t.Fatal(err)
	}
	if err := Promote(path, "v0.2.0"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	want := State{
		Active: "b", ActiveVersion: "v0.2.0",
		KnownGood: "b", KnownGoodVersion: "v0.2.0",
		BootAttempts: 0, RolledBackFrom: "",
	}
	if got != want {
		t.Errorf("after update promote:\n got %+v\nwant %+v", got, want)
	}
}

func TestPromoteRoutineBootKeepsRollbackMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	seed := State{
		Active: "a", ActiveVersion: "v0.1.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 1, RolledBackFrom: "v0.2.0",
	}
	if err := SaveState(path, seed); err != nil {
		t.Fatal(err)
	}
	if err := Promote(path, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadState(path)
	if got.RolledBackFrom != "v0.2.0" {
		t.Errorf("routine healthy boot cleared RolledBackFrom (= %q); only an update promote or dismiss may", got.RolledBackFrom)
	}
	if got.BootAttempts != 0 {
		t.Errorf("BootAttempts = %d, want 0", got.BootAttempts)
	}
}

func TestPromoteMissingStateIsNoop(t *testing.T) {
	if err := Promote(filepath.Join(t.TempDir(), "absent.json"), "v0.1.0"); err != nil {
		t.Errorf("Promote without a state file (dev mode) must be a no-op, got %v", err)
	}
}

func TestDismissRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(path, State{Active: "a", KnownGood: "a", RolledBackFrom: "v0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := DismissRollback(path); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadState(path)
	if got.RolledBackFrom != "" {
		t.Errorf("RolledBackFrom = %q after dismiss, want empty", got.RolledBackFrom)
	}
	// Missing state file: no-op, no error.
	if err := DismissRollback(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Errorf("dismiss without state file: %v", err)
	}
}
