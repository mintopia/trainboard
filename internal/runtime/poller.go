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
// single goroutine; a fetch outlasting the refresh interval only pushes the
// next poll back by its own duration (cadence drift, never concurrency).
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

	// failures counts consecutive polls that have left the snapshot in
	// StateError. It drives the fast-retry backoff in Run and resets to
	// zero the moment a poll lands in any non-error state.
	failures int

	// test seams
	now      func() time.Time
	pollDone chan<- struct{}
}

// errorBackoffSteps are the delays applied for the 1st, 2nd and 3rd+
// consecutive poll failures (StateError), per issue #40: 2s, then 5s, then
// 10s forever until recovery. Index 0 is unused (zero failures means the
// normal configured interval applies, handled separately in nextDelay).
var errorBackoffSteps = [...]time.Duration{
	0, 2 * time.Second, 5 * time.Second, 10 * time.Second,
}

// nextDelay computes how long Run should wait before the next poll, given
// how many consecutive polls have just left the snapshot in StateError.
// consecutiveFailures <= 0 means the last poll was healthy: the normal
// configured interval applies. Otherwise the short backoff table above
// applies (holding at its last step, 10s, for every failure beyond the
// third), always capped so the retry never waits longer than the configured
// interval — a short RefreshSeconds must not be defeated by the backoff.
func nextDelay(consecutiveFailures int, interval time.Duration) time.Duration {
	if consecutiveFailures <= 0 {
		return interval
	}
	step := consecutiveFailures
	if step >= len(errorBackoffSteps) {
		step = len(errorBackoffSteps) - 1
	}
	backoff := errorBackoffSteps[step]
	if backoff > interval {
		return interval
	}
	return backoff
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

// Run polls immediately, then repeats until ctx is cancelled. While the
// snapshot is healthy, polls follow the configured interval; while it's in
// StateError, Run retries on a short backoff (2s, 5s, then every 10s, see
// nextDelay) so a boot-time fetch failure — e.g. WiFi not yet associated —
// recovers within seconds of connectivity rather than waiting out a full
// refresh interval.
//
// This uses a fresh time.NewTimer per iteration rather than a single
// time.Ticker, because the wait between polls is no longer constant: the
// interval is now measured poll-to-poll (start the wait once pollOnce
// returns) rather than on fixed wall-clock ticks. A slow fetch therefore
// pushes the next poll back by its own duration instead of the ticker
// silently dropping a tick — acceptable drift for a display refresh, and it
// keeps the one-poll-at-a-time invariant obvious from the code.
func (p *Poller) Run(ctx context.Context) {
	p.pollOnce(ctx)
	for {
		t := time.NewTimer(nextDelay(p.failures, p.interval))
		select {
		case <-ctx.Done():
			t.Stop()
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

	if next.State == board.StateError {
		p.failures++
	} else {
		p.failures = 0
	}

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
