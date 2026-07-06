// Package runtime owns time and concurrency for the board: the Darwin
// poller publishing immutable snapshots through an atomic pointer, the
// fixed-rate render loop consuming them lock-free, and the classification
// of fetch results into on-screen states. board stays pure; data does the
// fetching; runtime is where they meet.
package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"crypto/x509"
	"errors"
	"strings"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
)

// StaleGrace is how long the last good board keeps showing through fetch
// failures before the Error scene takes over (ADR 0003 staleness window).
const StaleGrace = 5 * time.Minute

// Classify maps a fetch result onto the next snapshot per the M1C design's
// classification table. prev is the currently published snapshot (nil before
// the first fetch resolves). Inside the stale grace it returns prev itself,
// so the render loop sees an unchanged frame.
func Classify(b *data.Board, fetchErr error, prev *board.Snapshot, now time.Time) *board.Snapshot {
	if fetchErr == nil {
		st := board.StateDepartures
		if len(b.Departures) == 0 {
			st = board.StateNoServices
		}
		return &board.Snapshot{Board: b, State: st, FetchedAt: now}
	}

	if isClockError(fetchErr) {
		s := &board.Snapshot{State: board.StateClockNotSynced, Fault: obs.FaultClockNotSynced}
		if prev != nil {
			s.Board, s.FetchedAt = prev.Board, prev.FetchedAt
		}
		return s
	}

	if prev != nil &&
		(prev.State == board.StateDepartures || prev.State == board.StateNoServices) &&
		now.Sub(prev.FetchedAt) < StaleGrace {
		return prev
	}

	fault := obs.FaultDarwinUnreachable
	if isAuthError(fetchErr) {
		fault = obs.FaultAuthRejected
	}
	s := &board.Snapshot{State: board.StateError, Fault: fault}
	if prev != nil {
		s.Board, s.FetchedAt = prev.Board, prev.FetchedAt
	}
	return s
}

// isClockError reports whether err is the pre-NTP x509 time-validity
// failure. It must never match generic transport/DNS errors: this state is
// excluded from M3's AP-fallback trigger.
func isClockError(err error) bool {
	var cie x509.CertificateInvalidError
	if errors.As(err, &cie) && cie.Reason == x509.Expired {
		return true
	}
	return strings.Contains(err.Error(), "x509: certificate has expired or is not yet valid")
}

// isAuthError reports whether the fetch failed on credentials rather than
// connectivity (Darwin's fault text for a bad token).
func isAuthError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "token") || strings.Contains(msg, "unauthor")
}
