// Package update implements M5 self-update logic.
package update

import (
	"errors"
	"io/fs"
)

// MaxBootAttempts is how many launcher execs a slot gets before the
// launcher gives up on it (spec §2: "3 strikes").
const MaxBootAttempts = 3

// BootDecision is Decide's verdict: which slot to exec, whether to pass
// --recovery, and the state the launcher must persist BEFORE exec (so a
// slot that segfaults instantly still burned its attempt).
type BootDecision struct {
	Slot string
	// Recovery selects the payload's --recovery mode (double fault: the
	// known-good slot itself never came healthy).
	Recovery bool
	// RolledBack reports that THIS decision performed the rollback flip,
	// for the launcher's log line.
	RolledBack bool
	State      State
}

// Decide implements the launcher's slot selection (spec §2). It is pure:
// the caller persists .State and execs .Slot.
func Decide(s State) BootDecision {
	s.BootAttempts++
	if s.BootAttempts <= MaxBootAttempts {
		return BootDecision{Slot: s.Active, State: s}
	}
	if s.Active != s.KnownGood {
		// Rollback: the pending slot never came healthy. Flip back to
		// known-good and record what we abandoned for the web UI.
		s.RolledBackFrom = s.ActiveVersion
		s.Active = s.KnownGood
		s.ActiveVersion = s.KnownGoodVersion
		s.BootAttempts = 1 // this boot is known-good's first attempt
		return BootDecision{Slot: s.Active, RolledBack: true, State: s}
	}
	// Double fault: known-good itself is failing. Enter recovery mode with
	// the counter reset so an operator-triggered restart from the recovery
	// web UI retries a normal boot.
	s.BootAttempts = 0
	return BootDecision{Slot: s.KnownGood, Recovery: true, State: s}
}

// Promote marks the running slot healthy: the payload calls this once its
// health check passes (render loop up + web self-probe OK, spec §2). A
// missing state file means this is not a slot install (dev mode,
// pre-migration device) and is a silent no-op.
//
// RolledBackFrom is cleared only on an UPDATE promote (KnownGood was
// pointing elsewhere): a routine healthy boot must not silently clear an
// unacknowledged rollback marker the operator hasn't seen yet.
func Promote(path, version string) error {
	s, err := LoadState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	wasUpdate := s.KnownGood != s.Active
	s.BootAttempts = 0
	s.ActiveVersion = version
	s.KnownGood = s.Active
	s.KnownGoodVersion = version
	if wasUpdate {
		s.RolledBackFrom = ""
	}
	return SaveState(path, s)
}

// DismissRollback clears the rollback marker (operator acknowledged it in
// the web UI). Missing state file is a no-op, same as Promote.
func DismissRollback(path string) error {
	s, err := LoadState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s.RolledBackFrom = ""
	return SaveState(path, s)
}
