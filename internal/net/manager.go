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
}

// ManagerDeps wires the Connectivity Manager to its collaborators. Every OS
// side effect goes through Driver/Check/Dnsmasq/Prereqs; Now/After are timer
// seams for tests.
type ManagerDeps struct {
	Driver  apDriver
	Check   *Check
	Dnsmasq *Dnsmasq
	Prereqs func(ctx context.Context) error
	AP      APConfig
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

	provMu sync.Mutex
	provAt time.Time
}

// NewManager builds a Manager publishing ManagerBoot until Run (Task 9)
// drives its first transition.
func NewManager(d ManagerDeps) *Manager {
	m := &Manager{d: d, retry: make(chan struct{}, 1)}
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

// Run drives the full boot -> STA/AP -> retry state machine until ctx is
// cancelled. Implemented by Task 9; for now it simply blocks on ctx.
func (m *Manager) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// toSTA attempts the configured client network end to end: Prereqs, then
// Driver.AttemptSTA, then the layered Check. It publishes STAConnecting with
// the failing Stage on any layer failure, or Online (and calls OnOnline) on
// full success. Any failure is returned so the caller (Task 9) decides
// whether to fall back to AP. Returns errNoWifiConfigured without touching
// Prereqs or the driver if no SSID is configured.
func (m *Manager) toSTA(ctx context.Context) error {
	sta := m.d.STA()
	if sta.SSID == "" {
		return errNoWifiConfigured
	}

	if m.d.Prereqs != nil {
		if err := m.d.Prereqs(ctx); err != nil {
			m.publish(Status{State: ManagerSTAConnecting, LastSTAErr: err.Error()})
			return err
		}
	}

	if err := m.d.Driver.AttemptSTA(ctx, sta); err != nil {
		m.publish(Status{State: ManagerSTAConnecting, LastSTAErr: err.Error()})
		return err
	}

	stage, err := m.d.Check.Evaluate(ctx)
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
