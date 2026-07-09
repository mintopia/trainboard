// Command trainboard-launcher is the stable boot shim (spec §2, issue #18):
// it reads the updater state, burns a boot attempt, selects a slot
// (rolling back or entering recovery per update.Decide), and exec()s the
// payload — process replacement, so systemd's WatchdogSec/NotifyAccess
// apply to the payload unchanged.
//
// This binary is deliberately tiny and is NEVER updated by A/B (deploy.md
// §Self-update). Its contract is frozen: TRAINBOARD_SLOTS /
// TRAINBOARD_STATE env overrides, argv passed through verbatim,
// --recovery appended on double fault.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/mintopia/trainboard/internal/update"
)

func main() {
	slots := envOr("TRAINBOARD_SLOTS", update.DefaultSlotsDir)
	statePath := envOr("TRAINBOARD_STATE", update.DefaultStatePath)
	if err := launch(slots, statePath, os.Args[1:], realExec); err != nil {
		// Exec of every candidate failed. Exit nonzero and let systemd's
		// Restart=always retry — the state file has already burned this
		// attempt, so persistent failure still converges via Decide.
		fmt.Fprintln(os.Stderr, "trainboard-launcher:", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// realExec replaces this process with the payload.
func realExec(bin string, argv, env []string) error {
	return syscall.Exec(bin, argv, env)
}

// launch is main's testable body. execFn never returns on success in
// production (process replacement); tests substitute a recorder.
func launch(slots, statePath string, passthrough []string, execFn func(bin string, argv, env []string) error) error {
	st := update.LoadStateOrDefault(statePath)
	dec := update.Decide(st)
	if dec.RolledBack {
		fmt.Fprintf(os.Stderr, "trainboard-launcher: rolling back to slot %s (%s failed %d boots)\n",
			dec.Slot, dec.State.RolledBackFrom, update.MaxBootAttempts)
	}
	if dec.Recovery {
		fmt.Fprintf(os.Stderr, "trainboard-launcher: double fault — entering recovery mode on slot %s\n", dec.Slot)
	}
	// Persist BEFORE exec: a payload that segfaults instantly must still
	// have burned its attempt. A failed write is a warning, not fatal —
	// booting something beats refusing to boot (spec §2).
	if err := update.SaveState(statePath, dec.State); err != nil {
		fmt.Fprintln(os.Stderr, "trainboard-launcher: warning: persisting state:", err)
	}

	args := append([]string{}, passthrough...)
	if dec.Recovery {
		args = append(args, "--recovery")
	}
	bin := filepath.Join(slots, dec.Slot, "trainboard")
	err := execFn(bin, append([]string{bin}, args...), os.Environ())
	if err == nil {
		return nil // tests only; a real exec never returns on success
	}
	// Exec itself failed (missing/corrupt binary — e.g. an interrupted
	// install). If we weren't already on known-good, fall straight back to
	// it now rather than waiting three boots.
	if dec.Slot != dec.State.KnownGood {
		fb := filepath.Join(slots, dec.State.KnownGood, "trainboard")
		// Flip and persist the rollback state BEFORE the fallback exec, the
		// same way Decide's three-strikes branch does. Without this, state
		// still says Active=<the slot whose exec just failed>; if the
		// fallback-exec'd known-good payload later passes its health check,
		// Promote does s.KnownGood = s.Active and blesses the CORRUPT slot
		// as known-good — the next apply would overwrite the only working
		// binary, bricking the device.
		dec.State = update.Rollback(dec.State)
		fmt.Fprintf(os.Stderr, "trainboard-launcher: exec %s: %v; rolling back state and falling back to %s\n", bin, err, fb)
		if err := update.SaveState(statePath, dec.State); err != nil {
			fmt.Fprintln(os.Stderr, "trainboard-launcher: warning: persisting rollback state:", err)
		}
		if err2 := execFn(fb, append([]string{fb}, passthrough...), os.Environ()); err2 != nil {
			return fmt.Errorf("exec %s: %w; fallback exec %s: %w", bin, err, fb, err2)
		}
		return nil
	}
	return fmt.Errorf("exec %s: %w", bin, err)
}
