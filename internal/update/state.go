// Package update implements M5 self-update: the A/B slot state file, slot
// selection, signed-manifest verification, GitHub release discovery, the
// download/verify/install pipeline, and the periodic update checker
// (docs/superpowers/specs/2026-07-09-m5-self-update-design.md).
package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Production filesystem layout (spec §2). The launcher and payload both
// default to these; tests and dev builds override via flags/env.
const (
	DefaultSlotsDir  = "/opt/trainboard/slots"
	DefaultStatePath = "/var/lib/trainboard/updater/state.json"
)

// State is the updater state document shared by the launcher (increments
// BootAttempts, performs rollback flips) and the payload (applies updates,
// promotes known-good). Writes are atomic (SaveState); the two writers never
// run concurrently — the launcher writes strictly before exec()ing the
// payload, and the payload is the only process alive after that.
type State struct {
	// Active is the slot the launcher execs: "a" or "b".
	Active string `json:"active"`
	// ActiveVersion is the version installed in Active, recorded at apply
	// time so the launcher never has to interrogate a binary for its
	// version (it may not even exec).
	ActiveVersion string `json:"active_version"`
	// KnownGood is the last slot that passed the payload health check. The
	// apply pipeline never writes this slot (double-fault guarantee).
	KnownGood        string `json:"known_good"`
	KnownGoodVersion string `json:"known_good_version"`
	// BootAttempts counts launcher execs of Active since the last healthy
	// start. The launcher increments it BEFORE exec; the payload's
	// health-check promotion resets it to 0.
	BootAttempts int `json:"boot_attempts"`
	// VersionFloor is the high-water mark of every accepted manifest's
	// min_version: replayed old manifests below it are rejected (spec §1
	// anti-rollback). Empty until the first release sets one.
	VersionFloor string `json:"version_floor"`
	// RolledBackFrom is set by the launcher when a rollback flip happens,
	// surfaced in the web UI, and cleared by an operator dismiss or the
	// next successful update promotion.
	RolledBackFrom string `json:"rolled_back_from"`
}

// DefaultState is the fresh-install (and corrupt-state fallback) state:
// slot a active and known-good, no attempts burned.
func DefaultState() State { return State{Active: "a", KnownGood: "a"} }

// validSlot reports whether s names one of the two slots.
func validSlot(s string) bool { return s == "a" || s == "b" }

// LoadState reads and validates the state at path. A missing file returns an
// error wrapping fs.ErrNotExist so callers can distinguish "not a slot
// install" (dev mode, pre-migration device) from a corrupt document.
func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("update: reading state: %w", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("update: parsing state %s: %w", path, err)
	}
	if !validSlot(s.Active) {
		return State{}, fmt.Errorf("update: state %s: invalid active slot %q", path, s.Active)
	}
	if !validSlot(s.KnownGood) {
		return State{}, fmt.Errorf("update: state %s: invalid known_good slot %q", path, s.KnownGood)
	}
	return s, nil
}

// LoadStateOrDefault is the launcher's tolerant read: ANY failure (missing,
// unreadable, corrupt, invalid slots) degrades to DefaultState rather than
// refusing to boot (spec §2: degrade, never refuse).
func LoadStateOrDefault(path string) State {
	s, err := LoadState(path)
	if err != nil {
		return DefaultState()
	}
	return s
}

// SaveState writes s atomically: temp file in the same directory, fsync,
// rename over path — the same pattern as config.saveRaw. The parent
// directory is created if missing (first boot after migration).
func SaveState(path string, s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("update: encoding state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("update: creating state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("update: creating temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: chmod temp state: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: writing temp state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: fsync temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("update: closing temp state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("update: renaming state into place: %w", err)
	}
	return nil
}
