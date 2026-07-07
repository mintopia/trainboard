package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"testing"
	"time"
)

func TestSoakZeroValueInactive(t *testing.T) {
	var s Soak
	if got := s.Remaining(time.Unix(1_600_000_000, 0)); got != 0 {
		t.Fatalf("zero-value Soak: Remaining = %v, want 0", got)
	}
}

func TestSoakStartExpiresAtDeadline(t *testing.T) {
	var s Soak
	now := time.Unix(1_600_000_000, 0)
	s.Start(time.Hour, now)

	if got := s.Remaining(now); got != time.Hour {
		t.Fatalf("at start: Remaining = %v, want 1h", got)
	}
	if got := s.Remaining(now.Add(30 * time.Minute)); got != 30*time.Minute {
		t.Fatalf("halfway: Remaining = %v, want 30m", got)
	}
	if got := s.Remaining(now.Add(time.Hour)); got != 0 {
		t.Fatalf("at deadline: Remaining = %v, want 0", got)
	}
	if got := s.Remaining(now.Add(2 * time.Hour)); got != 0 {
		t.Fatalf("past deadline: Remaining = %v, want 0", got)
	}
}

func TestSoakCancel(t *testing.T) {
	var s Soak
	now := time.Unix(1_600_000_000, 0)
	s.Start(time.Hour, now)
	s.Cancel()
	if got := s.Remaining(now); got != 0 {
		t.Fatalf("after cancel: Remaining = %v, want 0", got)
	}
}

func TestSoakCancelWhenIdleIsNoop(t *testing.T) {
	var s Soak
	s.Cancel() // must not panic
	if got := s.Remaining(time.Unix(1_600_000_000, 0)); got != 0 {
		t.Fatalf("Remaining = %v, want 0", got)
	}
}

func TestSoakRestartResetsDeadline(t *testing.T) {
	var s Soak
	now := time.Unix(1_600_000_000, 0)
	s.Start(time.Hour, now)
	s.Start(4*time.Hour, now.Add(30*time.Minute)) // restart while active
	if got := s.Remaining(now.Add(30 * time.Minute)); got != 4*time.Hour {
		t.Fatalf("after restart: Remaining = %v, want 4h", got)
	}
}
