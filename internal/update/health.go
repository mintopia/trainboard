package update

import (
	"context"
	"log/slog"
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

	// probeEvery overrides the probe retry interval (tests); 0 = 2s.
	probeEvery time.Duration
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
