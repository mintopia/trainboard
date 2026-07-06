package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

type fakeFlusher struct {
	mu        sync.Mutex
	flushes   int
	lastFrame []byte
	contrasts []byte
}

func (f *fakeFlusher) Flush(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
	f.lastFrame = append([]byte(nil), p...)
	return nil
}

func (f *fakeFlusher) SetContrast(l byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contrasts = append(f.contrasts, l)
	return nil
}

func (f *fakeFlusher) stats() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes, len(f.contrasts)
}

func mustBoardFonts(t *testing.T) *board.Fonts {
	t.Helper()
	f, err := board.LoadFonts()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func powersavingCfg() config.Config {
	cfg := testCfg()
	cfg.Powersaving.Enabled = true // 23:00–07:00 @ 32 (defaults)
	return cfg
}

func newTestLoop(t *testing.T, src func() *board.Snapshot, cfg config.Config) (*Loop, *fakeFlusher) {
	t.Helper()
	fl := &fakeFlusher{}
	log := obs.NewLogger(&strings.Builder{}, nil, slog.LevelInfo)
	return NewLoop(src, fl, cfg, mustBoardFonts(t), "v1", log), fl
}

func TestStepFlushesFullFrameEveryTick(t *testing.T) {
	l, fl := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())
	day := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := l.step(day.Add(time.Duration(i) * TickInterval)); err != nil {
			t.Fatal(err)
		}
	}
	flushes, _ := fl.stats()
	if flushes != 3 {
		t.Fatalf("flushes = %d, want 3", flushes)
	}
	if len(fl.lastFrame) != board.W*board.H/2 {
		t.Fatalf("frame size = %d, want %d (full packed frame)", len(fl.lastFrame), board.W*board.H/2)
	}
}

func TestStepSetsContrastOnlyOnChange(t *testing.T) {
	l, fl := newTestLoop(t, func() *board.Snapshot { return nil }, powersavingCfg())
	day := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)    // normal: 255
	night := time.Date(2026, 7, 6, 23, 30, 0, 0, time.UTC) // saving: 32
	for i := 0; i < 5; i++ {
		_ = l.step(day.Add(time.Duration(i) * TickInterval))
	}
	_, n := fl.stats()
	if n != 1 {
		t.Fatalf("contrast commands after 5 same-brightness ticks = %d, want 1", n)
	}
	_ = l.step(night)
	_ = l.step(night.Add(TickInterval))
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if len(fl.contrasts) != 2 || fl.contrasts[1] != 32 {
		t.Fatalf("contrasts = %v, want [255 32]", fl.contrasts)
	}
}

func TestStepRebuildsSceneOnSnapshotChange(t *testing.T) {
	var mu sync.Mutex
	snap := (*board.Snapshot)(nil)
	src := func() *board.Snapshot { mu.Lock(); defer mu.Unlock(); return snap }
	l, fl := newTestLoop(t, src, testCfg())
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	_ = l.step(now)
	initFrame := append([]byte(nil), fl.lastFrame...)
	mu.Lock()
	snap = &board.Snapshot{State: board.StateDepartures, Board: goodBoard(2), FetchedAt: now}
	mu.Unlock()
	_ = l.step(now.Add(TickInterval))
	if string(initFrame) == string(fl.lastFrame) {
		t.Fatal("frame must change when the snapshot changes")
	}
	if l.tick != 1 {
		t.Fatalf("tick = %d, want 1 (reset to 0 on swap, then incremented)", l.tick)
	}
}

// The concurrency contract under the race detector: poller publishing while
// the loop reads and flushes.
func TestPollerAndLoopConcurrently(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(3)}}}
	p, _ := newTestPoller(f)
	l, fl := newTestLoop(t, p.Snapshot, testCfg())
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.Run(ctx) }()
	go func() {
		defer wg.Done()
		now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
		for i := 0; i < 200; i++ {
			_ = l.step(now.Add(time.Duration(i) * TickInterval))
		}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()
	if fls, _ := fl.stats(); fls != 200 {
		t.Fatalf("flushes = %d, want 200", fls)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	l, _ := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on ctx cancel")
	}
}

func TestStepEmitsPeriodicFrameTiming(t *testing.T) {
	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(&strings.Builder{}, ring, slog.LevelInfo)
	fl := &fakeFlusher{}
	l := NewLoop(func() *board.Snapshot { return nil }, fl, testCfg(), mustBoardFonts(t), "v1", log)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i <= timingEveryTicks; i++ {
		if err := l.step(now.Add(time.Duration(i) * TickInterval)); err != nil {
			t.Fatal(err)
		}
	}
	var timings int
	for _, e := range ring.Events() {
		if e.Msg == "frame timing" {
			timings++
		}
	}
	if timings != 1 {
		t.Fatalf("frame-timing events = %d, want exactly 1 after %d ticks", timings, timingEveryTicks+1)
	}
}
