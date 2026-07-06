package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
)

var t0 = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

func goodBoard(n int) *data.Board {
	b := &data.Board{LocationName: "Paddington", CRS: "PAD"}
	for i := 0; i < n; i++ {
		b.Departures = append(b.Departures, data.Departure{ScheduledTime: fmt.Sprintf("12:%02d", i)})
	}
	return b
}

func TestClassifySuccessWithDepartures(t *testing.T) {
	s := Classify(goodBoard(3), nil, nil, t0)
	if s.State != board.StateDepartures || s.Fault != obs.FaultNone || !s.FetchedAt.Equal(t0) || len(s.Board.Departures) != 3 {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestClassifySuccessEmptyIsNoServices(t *testing.T) {
	s := Classify(goodBoard(0), nil, nil, t0)
	if s.State != board.StateNoServices || s.Board == nil {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestClassifyNeverSucceededErrorIsError(t *testing.T) {
	s := Classify(nil, errors.New("dial tcp: connection refused"), nil, t0)
	if s.State != board.StateError || s.Fault != obs.FaultDarwinUnreachable {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestClassifyAuthErrorIsE02(t *testing.T) {
	s := Classify(nil, errors.New(`soap fault: "Invalid Access Token"`), nil, t0)
	if s.State != board.StateError || s.Fault != obs.FaultAuthRejected {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestClassifyStaleGraceKeepsPrevSnapshot(t *testing.T) {
	prev := Classify(goodBoard(2), nil, nil, t0)
	s := Classify(nil, errors.New("timeout"), prev, t0.Add(StaleGrace-time.Second))
	if s != prev {
		t.Fatalf("inside grace the previous snapshot must be returned unchanged; got %+v", s)
	}
}

func TestClassifyGraceExpiredIsError(t *testing.T) {
	prev := Classify(goodBoard(2), nil, nil, t0)
	s := Classify(nil, errors.New("timeout"), prev, t0.Add(StaleGrace))
	if s.State != board.StateError || s.Fault != obs.FaultDarwinUnreachable {
		t.Fatalf("at the 5-minute edge the state must become Error; got %+v", s)
	}
	if s.Board == nil || len(s.Board.Departures) != 2 {
		t.Fatal("error snapshot should still carry the last good board")
	}
}

func TestClassifyGraceDoesNotApplyAfterErrorState(t *testing.T) {
	prevErr := &board.Snapshot{State: board.StateError, Fault: obs.FaultDarwinUnreachable, FetchedAt: t0}
	s := Classify(nil, errors.New("timeout"), prevErr, t0.Add(time.Second))
	if s.State != board.StateError {
		t.Fatalf("grace applies only to good-data snapshots; got %+v", s)
	}
}

func TestClassifyX509IsClockNotSynced(t *testing.T) {
	certErr := x509.CertificateInvalidError{Reason: x509.Expired}
	wrapped := &url.Error{Op: "Post", URL: "https://lite.realtime.nationalrail.co.uk", Err: fmt.Errorf("tls: %w", certErr)}
	prev := Classify(goodBoard(1), nil, nil, t0)
	s := Classify(nil, wrapped, prev, t0.Add(time.Hour))
	if s.State != board.StateClockNotSynced || s.Fault != obs.FaultClockNotSynced {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.Board == nil {
		t.Fatal("clock-not-synced must carry the previous board")
	}
}

func TestClassifyX509ByMessage(t *testing.T) {
	err := errors.New("Post \"https://…\": x509: certificate has expired or is not yet valid: current time 1970-01-01T00:00:10Z is before 2025-01-01")
	s := Classify(nil, err, nil, t0)
	if s.State != board.StateClockNotSynced {
		t.Fatalf("snapshot = %+v", s)
	}
}
