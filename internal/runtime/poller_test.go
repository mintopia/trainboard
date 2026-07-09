package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
)

// scriptFetcher returns each result in sequence, then repeats the last.
type scriptFetcher struct {
	mu      sync.Mutex
	results []fetchResult
	i       int
	lastReq data.Request
}

type fetchResult struct {
	b   *data.Board
	err error
}

func (s *scriptFetcher) Fetch(_ context.Context, r data.Request) (*data.Board, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReq = r
	res := s.results[s.i]
	if s.i < len(s.results)-1 {
		s.i++
	}
	return res.b, res.err
}

func testCfg() config.Config {
	cfg := config.Default()
	cfg.Board.Origin = "PAD"
	cfg.Board.Services = 3
	cfg.Board.RefreshSeconds = 15 // min valid; tests drive polls manually anyway
	return cfg
}

func newTestPoller(f Fetcher) (*Poller, *obs.Ring) {
	ring := obs.NewRing(64)
	log := obs.NewLogger(&strings.Builder{}, ring, slog.LevelInfo)
	return NewPoller(f, testCfg(), log), ring
}

func TestPollOncePublishesSnapshot(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(5)}}}
	p, _ := newTestPoller(f)
	if p.Snapshot() != nil {
		t.Fatal("snapshot must be nil before first poll")
	}
	p.pollOnce(context.Background())
	s := p.Snapshot()
	if s == nil || s.State != board.StateDepartures {
		t.Fatalf("snapshot = %+v", s)
	}
	// Filter applied: Services=3 caps 5 departures to 3.
	if len(s.Board.Departures) != 3 {
		t.Fatalf("MaxServices filter not applied: %d departures", len(s.Board.Departures))
	}
	// Request derived from config with NumRows pinned to 10.
	if f.lastReq.OriginCRS != "PAD" || f.lastReq.NumRows != 10 {
		t.Fatalf("request = %+v", f.lastReq)
	}
}

func TestPollOnceStateTransitionLogged(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(2)}, {err: errors.New("boom")}}}
	p, ring := newTestPoller(f)
	p.now = func() time.Time { return t0 }
	p.pollOnce(context.Background())
	p.now = func() time.Time { return t0.Add(StaleGrace + time.Minute) }
	p.pollOnce(context.Background())
	if s := p.Snapshot(); s.State != board.StateError {
		t.Fatalf("state = %v, want error", s.State)
	}
	var transition bool
	for _, e := range ring.Events() {
		if e.Msg == "state transition" && e.Attrs["from"] == "departures" && e.Attrs["to"] == "error" {
			transition = true
		}
	}
	if !transition {
		t.Fatalf("missing state-transition event; ring = %+v", ring.Events())
	}
}

func TestPollOnceStaleGraceKeepsSnapshotIdentity(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(2)}, {err: errors.New("boom")}}}
	p, _ := newTestPoller(f)
	p.now = func() time.Time { return t0 }
	p.pollOnce(context.Background())
	first := p.Snapshot()
	p.now = func() time.Time { return t0.Add(time.Minute) }
	p.pollOnce(context.Background())
	if p.Snapshot() != first {
		t.Fatal("inside grace the identical snapshot pointer must stay published")
	}
}

func TestRunPollsUntilCancelled(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(1)}}}
	p, _ := newTestPoller(f)
	done := make(chan struct{}, 8)
	p.pollDone = done
	ctx, cancel := context.WithCancel(context.Background())
	go p.Run(ctx)
	<-done // first immediate poll
	cancel()
	if p.Snapshot() == nil {
		t.Fatal("Run must publish after its immediate first poll")
	}
}

// TestNextDelay exercises the pure backoff function directly: 2s, 5s, 10s,
// then 10s forever while in error state, capped at the configured interval,
// and reset to the full interval once the failure streak is broken.
func TestNextDelay(t *testing.T) {
	tests := []struct {
		name                string
		consecutiveFailures int
		interval            time.Duration
		want                time.Duration
	}{
		{"no failures uses configured interval", 0, 60 * time.Second, 60 * time.Second},
		{"first failure", 1, 60 * time.Second, 2 * time.Second},
		{"second failure", 2, 60 * time.Second, 5 * time.Second},
		{"third failure", 3, 60 * time.Second, 10 * time.Second},
		{"fourth failure holds at 10s", 4, 60 * time.Second, 10 * time.Second},
		{"tenth failure still holds at 10s", 10, 60 * time.Second, 10 * time.Second},
		{"third failure capped by a short interval", 3, 8 * time.Second, 8 * time.Second},
		{"first failure never exceeds interval", 1, 60 * time.Second, 2 * time.Second},
		{"negative failures treated as none", -1, 30 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextDelay(tt.consecutiveFailures, tt.interval); got != tt.want {
				t.Fatalf("nextDelay(%d, %s) = %s, want %s", tt.consecutiveFailures, tt.interval, got, tt.want)
			}
		})
	}
}

// TestPollOnceTracksConsecutiveFailures verifies the failure streak used to
// drive the backoff resets to zero the moment a poll leaves StateError.
func TestPollOnceTracksConsecutiveFailures(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{
		{err: errors.New("boom")},
		{err: errors.New("boom")},
		{b: goodBoard(1)},
		{err: errors.New("boom")},
	}}
	p, _ := newTestPoller(f)
	p.now = func() time.Time { return t0 }

	p.pollOnce(context.Background())
	if p.failures != 1 {
		t.Fatalf("after 1st error, failures = %d, want 1", p.failures)
	}
	p.pollOnce(context.Background())
	if p.failures != 2 {
		t.Fatalf("after 2nd error, failures = %d, want 2", p.failures)
	}
	p.pollOnce(context.Background())
	if p.failures != 0 {
		t.Fatalf("after recovery, failures = %d, want reset to 0", p.failures)
	}
	// Push past the stale grace so the next error actually reclassifies to
	// StateError instead of holding the last-good snapshot.
	p.now = func() time.Time { return t0.Add(StaleGrace + time.Minute) }
	p.pollOnce(context.Background())
	if p.failures != 1 {
		t.Fatalf("after a fresh error, failures = %d, want 1", p.failures)
	}
}

// TestPokeTriggersImmediatePollBypassingBackoff proves Poke short-circuits
// the error backoff entirely: without it, the retry would wait the full 2s
// first-failure backoff; Poke must deliver the retry almost immediately.
func TestPokeTriggersImmediatePollBypassingBackoff(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{err: errors.New("boom")}, {b: goodBoard(1)}}}
	p, _ := newTestPoller(f) // testCfg's interval is 15s; first backoff step is 2s
	done := make(chan struct{}, 8)
	p.pollDone = done
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	<-done // first, immediate poll: fails
	if s := p.Snapshot(); s == nil || s.State != board.StateError {
		t.Fatalf("snapshot after first poll = %+v, want error state", s)
	}

	p.Poke()
	select {
	case <-done:
		// Poke delivered the retry well inside the 2s backoff window.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Poke did not trigger an immediate poll bypassing the backoff wait")
	}
	if s := p.Snapshot(); s == nil || s.State != board.StateDepartures {
		t.Fatalf("snapshot after poked retry = %+v, want departures", s)
	}
}

// TestSetBeatIncrementsAcrossPolls verifies the beat callback fires once per
// Run loop iteration (Poke used here purely to force extra iterations
// deterministically, without waiting out the configured interval).
func TestSetBeatIncrementsAcrossPolls(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(1)}}}
	p, _ := newTestPoller(f)
	done := make(chan struct{}, 8)
	p.pollDone = done

	var mu sync.Mutex
	beats := 0
	p.SetBeat(func() {
		mu.Lock()
		beats++
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// The initial immediate poll (before Run's for-loop starts) races with
	// the loop's first beat() call in program order vs. this goroutine
	// observing it, so it is deliberately not asserted on here. What is
	// deterministic: each subsequent Poke-triggered poll is preceded, in
	// the same goroutine and therefore ordered, by that iteration's beat().
	<-done

	p.Poke()
	<-done
	mu.Lock()
	b1 := beats
	mu.Unlock()
	if b1 < 1 {
		t.Fatalf("beat count after one loop iteration = %d, want >= 1", b1)
	}

	// A single <-done after Poke can consume a STALE buffered signal from an
	// iteration that completed before the b1 read (the initial immediate
	// poll and the loop's first pass race the reads above) — observed as a
	// b1=b2 flake on loaded CI runners (run 29033120420). The poked
	// iteration's beat() is still guaranteed to arrive, so assert
	// eventually-with-deadline instead of pinning it to one done signal.
	p.Poke()
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		b2 := beats
		mu.Unlock()
		if b2 > b1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("beat count did not increase across iterations: b1=%d b2=%d", b1, b2)
		case <-done:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestRunRetriesFastAfterError proves the fix end-to-end: a failed poll is
// followed by a retry well inside the short backoff window rather than
// waiting out the full (much longer) configured refresh interval.
func TestRunRetriesFastAfterError(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{err: errors.New("boom")}, {b: goodBoard(1)}}}
	p, _ := newTestPoller(f) // testCfg's interval is 15s; the backoff must beat that by a wide margin
	done := make(chan struct{}, 8)
	p.pollDone = done
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	<-done // first, immediate poll: fails
	if s := p.Snapshot(); s == nil || s.State != board.StateError {
		t.Fatalf("snapshot after first poll = %+v, want error state", s)
	}

	select {
	case <-done: // fast retry after the 2s backoff
	case <-time.After(4 * time.Second):
		t.Fatal("expected a fast retry within the error backoff window, got none")
	}
	if s := p.Snapshot(); s == nil || s.State != board.StateDepartures {
		t.Fatalf("snapshot after retry = %+v, want departures", s)
	}
}
