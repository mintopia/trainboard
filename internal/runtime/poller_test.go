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
