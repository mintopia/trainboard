package update

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Health joins the two post-start health signals (spec §2) and promotes
// the running slot to known-good when both arrive within Deadline:
//
//   - FirstFrame: closed by the render loop's SetOnFirstFrame hook;
//   - Probe: a loopback HTTP request to the embedded web server.
//
// The health check runs INSIDE the payload — the launcher never judges
// health (#18); it only counts boots. If the deadline passes, Run returns
// without promoting and the launcher's attempt counter converges on a
// rollback.
//
// Promote trusts state.Active to name the running slot; it is the
// launcher's job to keep that invariant true on every path (including
// fast-fallback, which rolls the state back before exec), so this promote
// call is only as safe as the launcher's bookkeeping.
type Health struct {
	FirstFrame <-chan struct{}
	Probe      func(ctx context.Context) error
	Deadline   time.Duration
	StatePath  string
	Version    string
	Log        *slog.Logger

	// SlotsDir is the A/B slot root. When set, Run refuses to promote if
	// the running executable is not state.Active's binary — the guard for
	// the one path where the launcher's warn-not-fatal state write can
	// leave state.Active pointing at a slot that is NOT actually running
	// (fast-fallback with a failed boot-time SaveState): promoting there
	// would bless the broken slot. Empty = check disabled (dev mode).
	SlotsDir string

	// exe overrides os.Executable for tests; nil = os.Executable.
	exe func() (string, error)

	// probeEvery overrides the probe retry interval (tests); 0 = 2s.
	probeEvery time.Duration
}

// runningSlotOK reports whether it is safe to promote: either the slot
// cross-check is disabled (SlotsDir == ""), there is no state file yet
// (ErrNotExist — Promote no-ops anyway), or the running executable really
// is state.Active's binary. On a hard state-load error or a mismatch it
// logs a Warn and returns false so Run does not promote.
func (h Health) runningSlotOK() bool {
	if h.SlotsDir == "" {
		return true
	}
	st, err := LoadState(h.StatePath)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		h.Log.Warn("health check: loading state before promote", "error", err.Error())
		return false
	}

	exe := h.exe
	if exe == nil {
		exe = os.Executable
	}
	running, err := exe()
	if err != nil {
		h.Log.Warn("health check: resolving running executable before promote", "error", err.Error())
		return false
	}
	if resolved, err := filepath.EvalSymlinks(running); err == nil {
		running = resolved
	}
	running = filepath.Clean(running)
	want := filepath.Clean(filepath.Join(h.SlotsDir, st.Active, "trainboard"))
	if running != want {
		h.Log.Warn("health check: running executable is not the active slot binary; not promoting",
			"running", running, "active_slot_binary", want)
		return false
	}
	return true
}

// Run blocks until promotion, deadline, or ctx cancellation. Run it in a
// goroutine.
func (h Health) Run(ctx context.Context) {
	deadline := time.NewTimer(h.Deadline)
	defer deadline.Stop()

	select {
	case <-ctx.Done():
		return
	case <-deadline.C:
		h.Log.Warn("health check: no first frame before deadline; not promoting")
		return
	case <-h.FirstFrame:
	}

	every := h.probeEvery
	if every == 0 {
		every = 2 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := h.Probe(ctx); err == nil {
			if !h.runningSlotOK() {
				return
			}
			if err := Promote(h.StatePath, h.Version); err != nil {
				h.Log.Error("health check: promote failed", "error", err.Error())
				return
			}
			h.Log.Info("health check passed: slot promoted to known-good", "version", h.Version)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			h.Log.Warn("health check: web probe never succeeded before deadline; not promoting")
			return
		case <-t.C:
		}
	}
}
