package net

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mintopia/trainboard/internal/board"
)

// ManagerState classifies the Connectivity Manager's current phase.
type ManagerState int

const (
	// ManagerBoot is the state before the first STA/AP attempt.
	ManagerBoot ManagerState = iota
	// ManagerSTAConnecting is set while attempting (or retrying) the
	// configured client network; Status.Stage names the failing layer.
	ManagerSTAConnecting
	// ManagerOnline is set once the layered Check reports StageOK.
	ManagerOnline
	// ManagerAPFallback is set once the AP is up and verified; Status.Hotspot
	// is populated for the render loop.
	ManagerAPFallback
	// ManagerSTARetry is the mid tear-down-retry state: the AP has been torn
	// down to attempt STA again, but STA has not yet succeeded — the AP is
	// DOWN during this window (Task 9's run loop).
	ManagerSTARetry
)

// String names the state for logging (spec §Connectivity Manager states).
func (s ManagerState) String() string {
	switch s {
	case ManagerSTAConnecting:
		return "sta-connecting"
	case ManagerOnline:
		return "online"
	case ManagerAPFallback:
		return "ap-fallback"
	case ManagerSTARetry:
		return "sta-retry"
	default:
		return "boot"
	}
}

// errNoWifiConfigured is returned by toSTA when Deps.STA() reports no SSID
// (config has never been provisioned); the caller (Task 9's run loop)
// decides to fall back to AP without ever touching the driver.
var errNoWifiConfigured = errors.New("net: no wifi configured")

// Status is the Manager's published state: an atomic, immutable snapshot
// safe to read from any goroutine (e.g. the render loop, the web UI).
type Status struct {
	State ManagerState
	// Stage names the failing layer while State is STAConnecting or
	// APFallback (E06 detail); StageOK (empty) otherwise.
	Stage Stage
	// Hotspot is non-nil while the AP should be shown on-screen.
	Hotspot *board.Hotspot
	// LastSTAErr preserves the most recent STA failure text across an AP
	// restore, for the captive portal to display (M3b).
	LastSTAErr string
	// RadioBlocked is true when the STA Prereqs gate failed (wlan0
	// rfkill-soft-blocked / regulatory domain unset) — the render loop raises
	// E05 on-glass. Cleared on the next transition attempt (every publish is a
	// fresh Status), so it never leaks into a later Online/AP state.
	RadioBlocked bool
}

// ManagerDeps wires the Connectivity Manager to its collaborators. Every OS
// side effect goes through Driver/Check/Dnsmasq/Prereqs; Now/After are timer
// seams for tests.
type ManagerDeps struct {
	Driver  Driver
	Check   *Check
	Dnsmasq *Dnsmasq
	Prereqs func(ctx context.Context) error
	AP      APConfig
	// Runner is the exec seam cleanupOnCancel uses to kill any dhclient
	// daemon still renewing the STA lease on shutdown (issue #46 requirement
	// (c): the daemon must not outlive the manager). nil-tolerant — only
	// production wiring sets it; tests that never exercise ctx-cancel
	// teardown may leave it unset.
	Runner Runner
	// STA reads the current config; an empty SSID means no wifi configured.
	STA func() STAConfig
	// OnOnline pokes the poller once Online is published (Task 11).
	OnOnline func()
	// Beat is called every run-loop iteration as a watchdog heartbeat
	// (Task 11).
	Beat func()
	Log  *slog.Logger
	Now  func() time.Time
	// After returns a channel that fires after d; injected so tests can
	// return immediately-fired channels instead of real timers.
	After func(d time.Duration) <-chan time.Time
}

// Manager runs the connectivity state machine: try the configured STA
// network, fall back to a verified AP hotspot on failure, and retry STA
// periodically while the AP is up (Task 9's run loop). This file implements
// state publication and the two single transitions (toSTA, toAP); Run is
// completed by Task 9.
type Manager struct {
	d      ManagerDeps
	status atomic.Pointer[Status]
	retry  chan struct{}

	// firstAttempt and fastRetried gate the once-per-process instant-failure
	// fast retry (issue #47). They are owned exclusively by Run's single
	// goroutine — only ever read/written from bootSTA — so they need no mutex.
	// firstAttempt marks the very first STA attempt of the process (cleared
	// after it); fastRetried pins the fast retry to at most once per lifetime.
	firstAttempt bool
	fastRetried  bool

	provMu sync.Mutex
	provAt time.Time
}

// NewManager builds a Manager publishing ManagerBoot until Run (Task 9)
// drives its first transition.
func NewManager(d ManagerDeps) *Manager {
	m := &Manager{d: d, retry: make(chan struct{}, 1), firstAttempt: true}
	m.status.Store(&Status{State: ManagerBoot})
	return m
}

// Status returns the current published state. Lock-free.
func (m *Manager) Status() Status {
	return *m.status.Load()
}

// publish stores a new immutable snapshot.
func (m *Manager) publish(s Status) {
	m.status.Store(&s)
}

// RetryNow nudges the run loop to retry STA immediately instead of waiting
// for its next scheduled attempt. Non-blocking: a nudge already pending is
// enough, so a full channel is left alone.
func (m *Manager) RetryNow() {
	select {
	case m.retry <- struct{}{}:
	default:
	}
}

// NoteProvisioning records web/dnsmasq activity timestamps; the run loop
// (Task 9 / M3b) uses this to suppress STA retries while a user is actively
// using the captive portal.
func (m *Manager) NoteProvisioning(now time.Time) {
	m.provMu.Lock()
	m.provAt = now
	m.provMu.Unlock()
}

const (
	// onlineRecheckInterval is how often Run re-verifies connectivity while
	// ManagerOnline, via a cheap direct Check.Evaluate (not a full toSTA).
	onlineRecheckInterval = 30 * time.Second
	// apFallbackRetryWait is how long Run waits between STA retry attempts
	// while ManagerAPFallback, absent a RetryNow nudge.
	apFallbackRetryWait = 5 * time.Minute
	// apFallbackHeartbeatInterval is the cadence at which waitAPFallback
	// wakes up to call Beat while it's still short of apFallbackRetryWait —
	// otherwise a manager parked in AP fallback (a perfectly healthy state)
	// would go stale past the watchdog's managerBeatDeadline (cmd/trainboard
	// wiring: 90s at the time of writing) and get rebooted by systemd every
	// apFallbackRetryWait, wrecking provisioning. Must stay comfortably below
	// managerBeatDeadline; see connectivity.go's arithmetic comment.
	apFallbackHeartbeatInterval = 30 * time.Second
	// provisioningSuppressWindow: a NoteProvisioning call within this long of
	// a scheduled retry wake suppresses that cycle. RetryNow always bypasses
	// it — the operator explicitly asked to retry right now.
	provisioningSuppressWindow = 90 * time.Second
)

// staAttemptBound caps the AttemptSTA + Check.Evaluate transition inside
// toSTA (M3a spec: "bounded STA attempt, ≤45s through the layers") — without
// it, a hanging dhclient or an unbounded DNS LookupHost can block the run
// loop indefinitely, well past the watchdog heartbeat deadline. A package
// variable rather than a plain const purely so tests can shrink it instead
// of waiting out the real 45s; production wiring never touches it.
var staAttemptBound = 45 * time.Second

// fastRetryThreshold bounds "instant" for the once-per-process cold-start fast
// retry (issue #47): a FIRST STA attempt that fails faster than this almost
// certainly lost a race with wpa_supplicant's control socket coming up rather
// than genuinely failing to associate, so retrying once immediately is far
// cheaper than a full ~5-minute AP-fallback detour. The injected clock
// (m.now()) measures the attempt.
const fastRetryThreshold = 2 * time.Second

// managerPhase tracks Run's own loop position; distinct from the published
// Status (which is what callers such as the render loop or web UI observe).
type managerPhase int

const (
	phaseBoot managerPhase = iota
	phaseOnlineWait
	phaseAPWait
)

// Run drives the full boot -> STA/AP -> retry state machine until ctx is
// cancelled:
//
//  1. Boot: attempt toSTA once. On success, move to the online watch. On any
//     failure (including errNoWifiConfigured), fall back to toAP.
//  2. Online watch: every onlineRecheckInterval, re-run the cheap layered
//     Check directly; on failure, attempt one full toSTA reattempt; if that
//     also fails, fall back to toAP.
//  3. AP fallback: every apFallbackRetryWait (or immediately on RetryNow),
//     tear the AP down and retry STA — unless a recent NoteProvisioning
//     suppresses the cycle (RetryNow always bypasses suppression). A failed
//     retry restores the AP, recording LastSTAErr from the STA failure.
//
// Escalation: toAP already retries once internally (Task 8's toAP/attemptAP
// pair). If that still fails here — at boot, after a failed online-watch
// reattempt, or after a failed AP-fallback retry — Run returns a non-nil
// error instead of ever calling Beat again. There is no safe software
// recovery from "neither STA nor a verified AP will come up"; the caller
// (cmd, Task 11) treats a Run exit as fatal to the watchdog heartbeat and
// lets the hardware watchdog reboot the device.
//
// ctx cancellation is checked at the top of every iteration and inside every
// wait; Run then returns nil, best-effort tearing the AP down (Driver.StopAP
// + Dnsmasq.Stop) if it was up when cancelled — the device is shutting down
// or restarting, not failing.
func (m *Manager) Run(ctx context.Context) error {
	phase := phaseBoot
	for {
		select {
		case <-ctx.Done():
			m.cleanupOnCancel()
			return nil
		default:
		}
		if m.d.Beat != nil {
			m.d.Beat()
		}

		switch phase {
		case phaseBoot:
			next, cancelled, err := m.runBoot(ctx)
			if cancelled {
				return nil
			}
			if err != nil {
				return err
			}
			phase = next

		case phaseOnlineWait:
			next, cancelled, err := m.runOnlineWait(ctx)
			if cancelled {
				m.cleanupOnCancel()
				return nil
			}
			if err != nil {
				return err
			}
			phase = next

		case phaseAPWait:
			next, cancelled, err := m.runAPWait(ctx)
			if cancelled {
				m.cleanupOnCancel()
				return nil
			}
			if err != nil {
				return err
			}
			phase = next
		}
	}
}

// runBoot performs the single boot-time STA attempt, falling back to AP on
// any failure (errNoWifiConfigured or a layered check failure). If ctx was
// cancelled while either driver call was in flight, the resulting error is
// treated as clean shutdown (best-effort AP teardown, cancelled=true), never
// escalation — see Run's ctx-cancellation contract.
func (m *Manager) runBoot(ctx context.Context) (next managerPhase, cancelled bool, err error) {
	if err := m.bootSTA(ctx); err == nil {
		return phaseOnlineWait, false, nil
	}
	if err := m.toAP(ctx); err != nil {
		if ctx.Err() != nil {
			m.cleanupOnCancel()
			return phaseBoot, true, nil
		}
		return phaseBoot, false, fmt.Errorf("net: manager: boot: AP fallback failed: %w", err)
	}
	return phaseAPWait, false, nil
}

// bootSTA runs the boot-time STA attempt with the once-per-process
// instant-failure fast retry (issue #47). If the very first STA attempt of the
// process fails within fastRetryThreshold — measured on the injected clock, so
// a cold wpa_supplicant control-socket race rather than a genuine association
// failure — it retries STA once immediately instead of paying a full
// ~5-minute AP-fallback detour; only if that also fails does the caller fall
// back to AP.
//
// The fast retry is skipped when ctx is already cancelled (a shutdown, not a
// race — see Run's ctx-cancellation contract), so this stays a clean no-op on
// the ctx-cancel-mid-attempt path. firstAttempt/fastRetried are Run-loop-owned
// (this method runs only on Run's single goroutine); no mutex.
func (m *Manager) bootSTA(ctx context.Context) error {
	start := m.now()
	staErr := m.toSTA(ctx)
	first := m.firstAttempt
	m.firstAttempt = false
	if staErr == nil {
		return nil
	}
	if !m.fastRetried && first && ctx.Err() == nil && m.now().Sub(start) < fastRetryThreshold {
		m.fastRetried = true
		if m.d.Log != nil {
			m.d.Log.Info("net: manager: first STA attempt failed instantly; fast retry")
		}
		return m.toSTA(ctx)
	}
	return staErr
}

// runOnlineWait waits onlineRecheckInterval (or ctx cancellation), then
// re-checks connectivity: a cheap direct Check.Evaluate first, escalating to
// a full toSTA reattempt and finally toAP on repeated failure.
//
// Check.Evaluate runs under a child context bounded to staAttemptBound —
// exactly the same parent-vs-child cancel discipline toSTA's own
// AttemptSTA/Check.Evaluate calls use (see toSTA's doc comment): a hanging
// probe must not be able to block the run loop indefinitely, but a plain
// attempt-bound timeout only ever expires the CHILD, so callers must keep
// checking the parent ctx (not any context this method constructs) to tell
// clean shutdown apart from an ordinary recheck failure.
func (m *Manager) runOnlineWait(ctx context.Context) (managerPhase, bool, error) {
	select {
	case <-ctx.Done():
		return phaseOnlineWait, true, nil
	case <-m.d.After(onlineRecheckInterval):
	}

	recheckCtx, cancel := context.WithTimeout(ctx, staAttemptBound)
	stage, _ := m.d.Check.Evaluate(recheckCtx)
	cancel()
	if stage == StageOK {
		return phaseOnlineWait, false, nil
	}

	if err := m.toSTA(ctx); err == nil {
		return phaseOnlineWait, false, nil
	}

	if err := m.toAP(ctx); err != nil {
		if ctx.Err() != nil {
			m.cleanupOnCancel()
			return phaseOnlineWait, true, nil
		}
		return phaseOnlineWait, false, fmt.Errorf("net: manager: online watch: AP fallback failed: %w", err)
	}
	return phaseAPWait, false, nil
}

// runAPWait waits apFallbackRetryWait (or an immediate RetryNow nudge, or
// ctx cancellation), applies the provisioning-suppression rule, and — unless
// suppressed — tears the AP down and retries STA once, restoring the AP
// (with LastSTAErr recorded) on failure.
//
// A failed AP restore gets ONE in-process retry — a fresh teardown then a
// full second toAP transition — before the existing fatal escalation (issue
// #48): a SIGKILL'd wpa_supplicant makes the first restore's cold bring-up
// miss its verification, and exiting fatally there traded a cheap in-process
// retry for a systemd-watchdog reboot (~10 minutes total observed on
// hardware). The retry reuses toAP, so a restore that only succeeds on the
// retry is still fully VERIFIED before APFallback is published; a second
// failure returns the same fatal error as before, keeping the watchdog
// backstop unchanged.
func (m *Manager) runAPWait(ctx context.Context) (managerPhase, bool, error) {
	viaRetry, cancelled := m.waitAPFallback(ctx)
	if cancelled {
		return phaseAPWait, true, nil
	}

	if !viaRetry && m.suppressed() {
		if m.d.Log != nil {
			m.d.Log.Info("net: manager: suppressing scheduled STA retry (provisioning in progress)")
		}
		return phaseAPWait, false, nil
	}

	m.publish(Status{State: ManagerSTARetry})
	if m.d.Dnsmasq != nil {
		_ = m.d.Dnsmasq.Stop(ctx)
	}
	if m.d.Driver != nil {
		_ = m.d.Driver.StopAP(ctx)
	}

	staErr := m.toSTA(ctx)
	if staErr == nil {
		return phaseOnlineWait, false, nil
	}

	if err := m.toAP(ctx); err != nil {
		if ctx.Err() != nil {
			m.cleanupOnCancel()
			return phaseAPWait, true, nil
		}
		if m.d.Log != nil {
			m.d.Log.Info("net: manager: AP restore failed; one in-process retry", "err", err)
		}
		// Beat before the retry so the extra toAP never stretches the
		// worst-case heartbeat gap past managerBeatDeadline (cmd/trainboard's
		// connectivity.go: 150s). Arithmetic, per that file's comment: the gap
		// from the loop-top Beat to THIS one is the old worst-case chain with
		// toAP regrown for the issue #48 post-daemon-start budget —
		//   30 (wait remainder) + 5 (StopAP) + 5 (Dnsmasq.Stop)
		//   + 5 (Prereqs) + 45 (staAttemptBound) + 40 (toAP w/ internal
		//   retry: 2 x ~15s attempts incl. 10s AP polls + teardown)
		//   = 130s < 150s.
		// The gap from this Beat to the next loop-top Beat is only the fresh
		// teardown (~10s) + the retry toAP (~40s) = ~50s. Without this Beat
		// the single chain would be ~180s > 150s and the watchdog would fire
		// mid-retry.
		if m.d.Beat != nil {
			m.d.Beat()
		}
		// Fresh teardown before re-running the full transition, mirroring
		// toAP's own internal retry discipline (AP is DOWN during this window).
		_ = m.d.Driver.StopAP(ctx)
		_ = m.d.Dnsmasq.Stop(ctx)
		if err := m.toAP(ctx); err != nil {
			if ctx.Err() != nil {
				m.cleanupOnCancel()
				return phaseAPWait, true, nil
			}
			return phaseAPWait, false, fmt.Errorf("net: manager: AP fallback retry: AP restore failed: %w", err)
		}
	}

	cur := m.Status()
	cur.LastSTAErr = staErr.Error()
	m.publish(cur)
	return phaseAPWait, false, nil
}

// waitAPFallback blocks for up to apFallbackRetryWait, an immediate RetryNow
// nudge (buffered-1, drained here by the receive itself), or ctx
// cancellation — whichever comes first.
//
// A manager parked here is healthy (the AP is up and verified), but the
// 5-minute budget alone is far longer than the watchdog's managerBeatDeadline
// (Beat is otherwise only called once per Run loop iteration, at the top),
// so this wakes on an apFallbackHeartbeatInterval cadence to call Beat and
// re-enter the wait — tracking the remaining budget via m.d.Now (the
// injected-clock seam, matching suppressed()) rather than consuming it. Each
// heartbeat wake is otherwise inert: it does not touch suppression state and
// is indistinguishable to the caller from having simply not woken yet.
func (m *Manager) waitAPFallback(ctx context.Context) (viaRetry, cancelled bool) {
	start := m.d.Now()
	for {
		remaining := apFallbackRetryWait - m.d.Now().Sub(start)
		if remaining < 0 {
			remaining = 0
		}
		wait := remaining
		if wait > apFallbackHeartbeatInterval {
			wait = apFallbackHeartbeatInterval
		}

		select {
		case <-ctx.Done():
			return false, true
		case <-m.retry:
			return true, false
		case <-m.d.After(wait):
			if m.d.Now().Sub(start) >= apFallbackRetryWait {
				return false, false
			}
			if m.d.Beat != nil {
				m.d.Beat()
			}
		}
	}
}

// suppressed reports whether NoteProvisioning was called within
// provisioningSuppressWindow of m.d.Now (falling back to the real clock if
// Now is unset, which only happens in incomplete production wiring — every
// test seam sets it).
func (m *Manager) suppressed() bool {
	m.provMu.Lock()
	at := m.provAt
	m.provMu.Unlock()
	if at.IsZero() {
		return false
	}
	return m.now().Sub(at) < provisioningSuppressWindow
}

// now reads the injected clock, falling back to the real clock only in
// incomplete production wiring (every test seam sets Now). The single point
// where internal/net logic reaches for the wall clock.
func (m *Manager) now() time.Time {
	if m.d.Now != nil {
		return m.d.Now()
	}
	return time.Now()
}

// cleanupOnCancel best-effort tears down on ctx cancellation — the device is
// shutting down or restarting, not failing, so errors here are not
// actionable. Two things happen, unconditionally and independently:
//
//   - any dhclient daemon left renewing the STA lease is killed (issue #46
//     requirement (c): a clean shutdown while Online must not leave the
//     daemon running indefinitely). killDHClient is idempotent and tolerates
//     "not running", so calling it regardless of ManagerState is correct
//     even when STA was never attempted this process.
//   - the AP is torn down, but only if it was actually up (ManagerAPFallback)
//     — StopAP/Dnsmasq.Stop on a never-started AP would be pure noise.
func (m *Manager) cleanupOnCancel() {
	bg := context.Background()
	if m.d.Runner != nil {
		killDHClient(bg, m.d.Runner, m.d.Log)
	}
	if m.Status().State != ManagerAPFallback {
		return
	}
	if m.d.Driver != nil {
		_ = m.d.Driver.StopAP(bg)
	}
	if m.d.Dnsmasq != nil {
		_ = m.d.Dnsmasq.Stop(bg)
	}
}

// toSTA attempts the configured client network end to end: Prereqs, then
// Driver.AttemptSTA, then the layered Check. It publishes STAConnecting with
// the failing Stage on any layer failure, or Online (and calls OnOnline) on
// full success. Any failure is returned so the caller (Task 9) decides
// whether to fall back to AP. Returns errNoWifiConfigured without touching
// Prereqs or the driver if no SSID is configured.
//
// AttemptSTA and Check.Evaluate run under a child context bounded to
// staAttemptBound (45s) — a hanging dhclient or an unbounded DNS lookup must
// not be able to block the run loop indefinitely. The child is derived from
// ctx (the caller's, ultimately Run's) so parent cancellation still
// propagates immediately, but a plain attempt-bound timeout only ever
// expires the CHILD: callers must keep checking the parent ctx (not any
// context toSTA constructs) to tell clean shutdown apart from an ordinary
// STA failure — see runBoot/runOnlineWait/runAPWait's ctx.Err() checks,
// which already do this correctly since they only ever see the parent.
func (m *Manager) toSTA(ctx context.Context) error {
	sta := m.d.STA()
	if sta.SSID == "" {
		return errNoWifiConfigured
	}

	if m.d.Prereqs != nil {
		if err := m.d.Prereqs(ctx); err != nil {
			m.publish(Status{State: ManagerSTAConnecting, LastSTAErr: err.Error(), RadioBlocked: true})
			return err
		}
	}

	attemptCtx, cancel := context.WithTimeout(ctx, staAttemptBound)
	defer cancel()

	if err := m.d.Driver.AttemptSTA(attemptCtx, sta); err != nil {
		m.publish(Status{State: ManagerSTAConnecting, LastSTAErr: err.Error()})
		return err
	}

	stage, err := m.d.Check.Evaluate(attemptCtx)
	if stage != StageOK {
		m.publish(Status{State: ManagerSTAConnecting, Stage: stage, LastSTAErr: err.Error()})
		return err
	}

	m.publish(Status{State: ManagerOnline})
	if m.d.OnOnline != nil {
		m.d.OnOnline()
	}
	return nil
}

// toAP brings up the AP and verifies it before publishing APFallback:
// Driver.StartAP -> Dnsmasq.Start -> verify (Driver.APActive true AND
// Dnsmasq.Alive true). A failed verification gets one full retry (StopAP +
// Dnsmasq.Stop, then the same sequence again); a second failure returns the
// error without ever publishing APFallback (Task 9 escalates).
func (m *Manager) toAP(ctx context.Context) error {
	if err := m.attemptAP(ctx); err == nil {
		return nil
	}

	// Full teardown before the single retry: AP is DOWN during this window.
	_ = m.d.Driver.StopAP(ctx)
	_ = m.d.Dnsmasq.Stop(ctx)

	return m.attemptAP(ctx)
}

// attemptAP runs one StartAP+Dnsmasq.Start+verify cycle, publishing
// APFallback only on full success.
func (m *Manager) attemptAP(ctx context.Context) error {
	if err := m.d.Driver.StartAP(ctx, m.d.AP); err != nil {
		return fmt.Errorf("net: toAP: StartAP: %w", err)
	}
	if err := m.d.Dnsmasq.Start(ctx); err != nil {
		return fmt.Errorf("net: toAP: Dnsmasq.Start: %w", err)
	}

	active, err := m.d.Driver.APActive(ctx)
	if err != nil {
		return fmt.Errorf("net: toAP: APActive: %w", err)
	}
	alive, err := m.d.Dnsmasq.Alive(ctx)
	if err != nil {
		return fmt.Errorf("net: toAP: Dnsmasq.Alive: %w", err)
	}
	if !active || !alive {
		return fmt.Errorf("net: toAP: verification failed (active=%v alive=%v)", active, alive)
	}

	m.publish(Status{
		State:   ManagerAPFallback,
		Hotspot: &board.Hotspot{SSID: m.d.AP.SSID, Addr: "192.168.4.1"},
	})
	return nil
}
