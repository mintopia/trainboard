package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"sync"
	"time"
)

// Soak is the burn-in soak override: while active, the render loop bypasses
// scene rendering and cycles the panel full-white/full-black (spec:
// docs/superpowers/specs/2026-07-07-soak-mode-design.md). It is deliberately
// in-memory only — any process restart ends the soak, so a soak can never
// resume unattended after a crash or config apply.
//
// The zero value is ready to use (inactive). All methods take the clock as a
// parameter so tests inject time; there is no time.Now() in this file.
type Soak struct {
	mu       sync.Mutex
	deadline time.Time
}

// Start begins (or, while already active, re-arms) a soak ending at now+d.
func (s *Soak) Start(d time.Duration, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = now.Add(d)
}

// Cancel ends the soak immediately. Idle cancel is a no-op.
func (s *Soak) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = time.Time{}
}

// Remaining reports how long the soak has left at now; 0 means inactive
// (never started, cancelled, or expired).
func (s *Soak) Remaining(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deadline.IsZero() || !now.Before(s.deadline) {
		return 0
	}
	return s.deadline.Sub(now)
}
