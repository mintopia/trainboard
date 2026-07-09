package update

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/mintopia/trainboard/internal/config"
)

// Checker cadences (spec §3): a full release-feed check every checkEvery;
// the loop wakes every tickEvery so an opt-in auto-apply can catch the
// update window without waiting for the next 6h check.
const (
	checkEvery   = 6 * time.Hour
	tickEvery    = 10 * time.Minute
	initialDelay = 2 * time.Minute // let connectivity settle after boot
)

// Status is the updater's render-ready snapshot for the web UI / JSON API.
type Status struct {
	// Enabled reports whether the updater is usable at all on this device
	// (slot install present + keyring non-empty). The web UI hides the
	// whole Software section's controls when false.
	Enabled bool   `json:"enabled"`
	Running string `json:"running"`
	// Available is the newer release's version ("" = none known).
	Available string    `json:"available,omitempty"`
	NotesURL  string    `json:"notesUrl,omitempty"`
	LastCheck time.Time `json:"lastCheck"`
	LastError string    `json:"lastError,omitempty"`
	// RolledBackFrom surfaces the launcher's rollback marker (spec §3),
	// read live from the state file.
	RolledBackFrom string `json:"rolledBackFrom,omitempty"`
}

// Checker periodically discovers releases and (opt-in) auto-applies them
// inside the unattended-update window.
type Checker struct {
	client  *Client
	applier *Applier
	cfg     config.Config
	enabled bool
	log     *slog.Logger

	mu        sync.Mutex
	available *Release
	lastCheck time.Time
	lastErr   string

	// applyMu serializes ApplyNow end-to-end. It is separate from mu (which
	// stays short-hold so Status() remains responsive) because ApplyNow's
	// download/install pipeline can run for seconds and must not overlap:
	// tick's unattended auto-apply and a user-triggered web-handler call
	// both reach ApplyNow, and without this lock two near-simultaneous
	// calls could both see a non-nil cached release and run
	// applier.Apply concurrently against the same target slot.
	applyMu sync.Mutex
}

// NewChecker wires the checker. enabled=false (no slot install, empty
// keyring, or recovery mode) makes Run a no-op and Status report
// Enabled=false, but the struct is always safe to call.
func NewChecker(client *Client, applier *Applier, cfg config.Config, enabled bool, log *slog.Logger) *Checker {
	return &Checker{client: client, applier: applier, cfg: cfg, enabled: enabled, log: log}
}

// Run is the periodic loop. It exits when ctx is cancelled, immediately if
// the checker is disabled or the operator disabled checks. restart is
// invoked only after a successful unattended apply (auto-apply on, inside
// the window) — production wires the same clean-exit used by config apply.
func (c *Checker) Run(ctx context.Context, restart func()) {
	if !c.enabled || c.cfg.Update.DisableChecks {
		return
	}
	// Jittered initial delay: don't stampede the API at whole-fleet boot,
	// and let STA connectivity come up first.
	first := time.NewTimer(initialDelay + time.Duration(rand.Int63n(int64(time.Minute)))) //nolint:gosec // jitter, not crypto
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	c.tick(ctx, restart)

	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(ctx, restart)
		}
	}
}

// tick runs a feed check when one is due, then evaluates auto-apply.
func (c *Checker) tick(ctx context.Context, restart func()) {
	c.mu.Lock()
	due := time.Since(c.lastCheck) >= checkEvery
	c.mu.Unlock()
	if due {
		if err := c.CheckNow(ctx); err != nil {
			c.log.Warn("update check failed", "error", err.Error())
		}
	}

	c.mu.Lock()
	avail := c.available
	c.mu.Unlock()
	if avail == nil || !c.cfg.Update.AutoApply || !c.cfg.InUpdateWindow(time.Now()) {
		return
	}
	// Don't auto-apply a version the launcher just rolled back from: it's
	// still the newest release on the feed until a fix ships, so every
	// subsequent tick would otherwise find it "available" again and
	// reapply it forever, undoing the rollback nightly. A manual ApplyNow
	// (operator override, via the web UI) is deliberately NOT gated here —
	// only this unattended path is.
	if s, err := LoadState(c.applier.StatePath); err == nil && s.RolledBackFrom == avail.Version {
		c.log.Info("auto-apply suppressed: rolled-back version; waiting for a newer release", "version", avail.Version)
		return
	}
	c.log.Info("auto-applying update", "version", avail.Version)
	if err := c.ApplyNow(ctx); err != nil {
		c.log.Error("auto-apply failed", "error", err.Error())
		return
	}
	restart()
}

// CheckNow queries the release feed once and records the outcome. An
// "available" release is one that is strictly newer than the running
// version — except a non-semver running version ("dev") which any valid
// release beats.
func (c *Checker) CheckNow(ctx context.Context) error {
	rel, err := c.client.LatestRelease(ctx, c.cfg.Update.EffectiveChannel())

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastCheck = time.Now()
	if err != nil {
		c.lastErr = err.Error()
		c.available = nil
		return err
	}
	c.lastErr = ""
	c.available = nil
	if rel == nil {
		return nil
	}
	running := c.applier.Running
	if !semver.IsValid(running) || semver.Compare(rel.Version, running) > 0 {
		c.available = rel
	}
	return nil
}

// ApplyNow applies the last-found release (checking first if none is
// cached). It does NOT restart the process: the web handler renders its
// response and schedules the restart itself, exactly like config apply;
// only Run's unattended path restarts directly.
//
// ApplyNow is reachable concurrently from tick's unattended auto-apply and
// from a user-triggered web-handler call; applyMu serializes the whole
// call so only one download/install pipeline runs at a time. A second
// caller that blocks here does NOT simply find nothing to do once it
// wakes up: the winner clears c.available on success, but that only makes
// this caller retake the rel == nil branch below and call CheckNow again —
// and since c.applier.Running doesn't change until the process actually
// restarts onto the new slot, CheckNow will typically see the same
// release as still newer and repopulate c.available, so this caller goes
// on to redundantly re-run applier.Apply for the same release into the
// same target slot (Apply always targets otherSlot(KnownGood), which the
// first apply didn't move). That re-apply is wasted work (a second
// download) but not unsafe: it is now sequential, not concurrent, and
// Apply's writes are atomic.
func (c *Checker) ApplyNow(ctx context.Context) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	c.mu.Lock()
	rel := c.available
	c.mu.Unlock()
	if rel == nil {
		if err := c.CheckNow(ctx); err != nil {
			return err
		}
		c.mu.Lock()
		rel = c.available
		c.mu.Unlock()
	}
	if rel == nil {
		return errors.New("update: no update available")
	}
	if err := c.applier.Apply(ctx, rel); err != nil {
		c.mu.Lock()
		c.lastErr = err.Error()
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.available = nil
	c.lastErr = ""
	c.mu.Unlock()
	return nil
}

// Available reports whether a newer release is currently known, from the
// in-memory cache only — no disk I/O, no JSON parse. It exists for the
// render loop's per-frame hint-dot probe (25fps: Status()'s LoadState call
// is too expensive to run that often); Status() remains the richer
// snapshot for web requests, where a live RolledBackFrom read matters more
// than a few extra microseconds.
func (c *Checker) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.available != nil
}

// Status assembles the render-ready snapshot. RolledBackFrom is read live
// from the state file on every call (cheap: one small file) so the web UI
// sees a rollback the moment the launcher records it.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{
		Enabled:   c.enabled,
		Running:   c.applier.Running,
		LastCheck: c.lastCheck,
		LastError: c.lastErr,
	}
	if c.available != nil {
		st.Available = c.available.Version
		st.NotesURL = c.available.NotesURL
	}
	if s, err := LoadState(c.applier.StatePath); err == nil {
		st.RolledBackFrom = s.RolledBackFrom
	}
	return st
}
