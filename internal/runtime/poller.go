package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
)

// fetchTimeout bounds one Darwin round-trip. Polls can never overlap — not
// because of this value, but because Run calls pollOnce synchronously from a
// single goroutine; a fetch outlasting the refresh interval only delays the
// next tick (cadence drift, never concurrency).
const fetchTimeout = 30 * time.Second

// Fetcher is the data-client seam; *data.Client satisfies it.
type Fetcher interface {
	Fetch(ctx context.Context, r data.Request) (*data.Board, error)
}

// Poller fetches departures on the configured interval and publishes
// immutable snapshots through an atomic pointer. It never blocks the render
// loop and the render loop never blocks it.
type Poller struct {
	fetcher  Fetcher
	req      data.Request
	filter   data.Filter
	interval time.Duration
	log      *slog.Logger
	snap     atomic.Pointer[board.Snapshot]

	// test seams
	now      func() time.Time
	pollDone chan<- struct{}
}

// NewPoller derives the Darwin request and client-side filter from cfg.
// NumRows stays pinned at 10 (the LDBWS WithDetails cap): display trimming
// happens in the filter via cfg.Board.Services. cfg must have passed
// Validate (RefreshSeconds >= 15); a zero interval would panic in Run.
func NewPoller(f Fetcher, cfg config.Config, log *slog.Logger) *Poller {
	return &Poller{
		fetcher: f,
		req: data.Request{
			OriginCRS:         cfg.Board.Origin,
			DestinationCRS:    cfg.Board.Destination,
			NumRows:           10,
			TimeWindowMinutes: cfg.Board.TimeWindowMinutes,
		},
		filter: data.Filter{
			Platforms:    cfg.Board.Platforms,
			TOCs:         cfg.Board.TOCs,
			MaxServices:  cfg.Board.Services,
			CutoffHours:  cfg.Board.CutoffHours,
			Replacements: cfg.Board.Replacements,
		},
		interval: time.Duration(cfg.Board.RefreshSeconds) * time.Second,
		log:      log,
		now:      time.Now,
	}
}

// Snapshot returns the currently published snapshot (nil before the first
// poll completes). Lock-free.
func (p *Poller) Snapshot() *board.Snapshot {
	return p.snap.Load()
}

// Run polls immediately, then on every interval tick until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	p.pollOnce(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.pollOnce(ctx)
		}
	}
}

// pollOnce must only be called from Run's goroutine: the one-poll-at-a-time
// invariant is structural (sequential caller), not enforced with a lock.
func (p *Poller) pollOnce(ctx context.Context) {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	b, err := p.fetcher.Fetch(fctx, p.req)
	if err == nil {
		b = p.filter.Apply(b)
	}
	prev := p.snap.Load()
	next := Classify(b, err, prev, p.now())
	p.snap.Store(next)

	switch {
	case err != nil:
		p.log.Warn("fetch failed", "err", err.Error(), "state", next.State.String())
	default:
		p.log.Info("fetched", "departures", len(next.Board.Departures), "state", next.State.String())
	}
	if prev != nil && prev.State != next.State {
		p.log.Info("state transition", "from", prev.State.String(), "to", next.State.String())
	} else if prev == nil {
		p.log.Info("state transition", "from", "initialising", "to", next.State.String())
	}
	if p.pollDone != nil {
		select {
		case p.pollDone <- struct{}{}:
		default:
		}
	}
}
