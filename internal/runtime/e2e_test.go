package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/obs"
)

// TestEndToEndScriptedOutcomes drives config → fetch → classify → scene →
// flush across the spec's fetch-outcome script and asserts the state the
// screen is in after each poll.
func TestEndToEndScriptedOutcomes(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{
		{b: goodBoard(3)},                      // 1: departures
		{b: goodBoard(0)},                      // 2: no services
		{err: errors.New("dial tcp: refused")}, // 3: error inside grace → keeps NoServices
		{err: errors.New("dial tcp: refused")}, // 4: error past grace → Error
		{b: goodBoard(2)},                      // 5: recovery
	}}
	ring := obs.NewRing(64)
	log := obs.NewLogger(&strings.Builder{}, ring, slog.LevelInfo)
	p := NewPoller(f, testCfg(), log)

	clock := t0
	p.now = func() time.Time { return clock }

	l, fl := newTestLoop(t, p.Snapshot, testCfg())

	steps := []struct {
		advance time.Duration
		want    board.State
	}{
		{0, board.StateDepartures},
		{time.Minute, board.StateNoServices},
		{time.Minute, board.StateNoServices}, // stale grace holds
		{StaleGrace, board.StateError},
		{time.Minute, board.StateDepartures},
	}
	for i, st := range steps {
		clock = clock.Add(st.advance)
		p.pollOnce(context.Background())
		snap := p.Snapshot()
		if snap.State != st.want {
			t.Fatalf("step %d: state = %v, want %v", i+1, snap.State, st.want)
		}
		if err := l.step(clock); err != nil {
			t.Fatal(err)
		}
	}
	if flushes, _ := fl.stats(); flushes != len(steps) {
		t.Fatalf("flushes = %d, want %d", flushes, len(steps))
	}
	// The ring recorded every transition of the journey.
	var transitions []string
	for _, e := range ring.Events() {
		if e.Msg == "state transition" {
			transitions = append(transitions, e.Attrs["from"]+"→"+e.Attrs["to"])
		}
	}
	want := []string{"initialising→departures", "departures→no-services", "no-services→error", "error→departures"}
	if strings.Join(transitions, ",") != strings.Join(want, ",") {
		t.Fatalf("transitions = %v, want %v", transitions, want)
	}
}
