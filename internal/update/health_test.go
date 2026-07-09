package update

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func healthState(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	seed := State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 1}
	if err := SaveState(path, seed); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHealthPromotesWhenBothSignalsArrive(t *testing.T) {
	path := healthState(t)
	ff := make(chan struct{})
	close(ff) // first frame already flushed
	h := Health{
		FirstFrame: ff,
		Probe:      func(_ context.Context) error { return nil },
		Deadline:   2 * time.Second,
		StatePath:  path, Version: "v0.2.0", Log: testLogger(),
	}
	h.Run(context.Background())
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.KnownGood != "b" || st.KnownGoodVersion != "v0.2.0" || st.BootAttempts != 0 {
		t.Errorf("not promoted: %+v", st)
	}
}

func TestHealthGivesUpAtDeadline(t *testing.T) {
	path := healthState(t)
	h := Health{
		FirstFrame: make(chan struct{}), // never fires
		Probe:      func(_ context.Context) error { return errors.New("down") },
		Deadline:   150 * time.Millisecond,
		StatePath:  path, Version: "v0.2.0", Log: testLogger(),
	}
	done := make(chan struct{})
	go func() { h.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not give up at deadline")
	}
	st, _ := LoadState(path)
	if st.KnownGood != "a" || st.BootAttempts != 1 {
		t.Errorf("promoted despite failed health check: %+v", st)
	}
}

func TestHealthPromoteRunningSlotGuard(t *testing.T) {
	t.Run("mismatch: running exe is not state.Active's binary", func(t *testing.T) {
		path := healthState(t) // Active=b, KnownGood=a
		slotsDir := t.TempDir()
		ff := make(chan struct{})
		close(ff)
		h := Health{
			FirstFrame: ff,
			Probe:      func(_ context.Context) error { return nil },
			Deadline:   2 * time.Second,
			StatePath:  path, Version: "v0.2.0", Log: testLogger(),
			SlotsDir: slotsDir,
			exe: func() (string, error) {
				return filepath.Join(slotsDir, "a", "trainboard"), nil
			},
		}
		h.Run(context.Background())
		st, err := LoadState(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.KnownGood != "a" || st.KnownGoodVersion != "v0.1.0" {
			t.Errorf("promoted despite running-slot mismatch: %+v", st)
		}
	})

	t.Run("match: running exe is state.Active's binary", func(t *testing.T) {
		path := healthState(t) // Active=b, KnownGood=a
		slotsDir := t.TempDir()
		ff := make(chan struct{})
		close(ff)
		h := Health{
			FirstFrame: ff,
			Probe:      func(_ context.Context) error { return nil },
			Deadline:   2 * time.Second,
			StatePath:  path, Version: "v0.2.0", Log: testLogger(),
			SlotsDir: slotsDir,
			exe: func() (string, error) {
				return filepath.Join(slotsDir, "b", "trainboard"), nil
			},
		}
		h.Run(context.Background())
		st, err := LoadState(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.KnownGood != "b" || st.KnownGoodVersion != "v0.2.0" {
			t.Errorf("not promoted despite running-slot match: %+v", st)
		}
	})
}

func TestHealthRetriesProbe(t *testing.T) {
	path := healthState(t)
	ff := make(chan struct{})
	close(ff)
	calls := 0
	h := Health{
		FirstFrame: ff,
		Probe: func(_ context.Context) error {
			calls++
			if calls < 3 {
				return errors.New("not yet")
			}
			return nil
		},
		Deadline:  5 * time.Second,
		StatePath: path, Version: "v0.2.0", Log: testLogger(),
	}
	h.probeEvery = 10 * time.Millisecond // test seam; defaults to 2s
	h.Run(context.Background())
	st, _ := LoadState(path)
	if st.KnownGood != "b" {
		t.Errorf("not promoted after probe retries: %+v", st)
	}
	if calls < 3 {
		t.Errorf("probe called %d times", calls)
	}
}
