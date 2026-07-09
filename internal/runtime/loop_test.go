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

func TestLoopSoakRendersUniformCyclingFrames(t *testing.T) {
	l, fl := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())
	soak := &Soak{}
	l.UseSoak(soak)

	base := time.Unix(1_600_000_000, 0) // Unix()/2 even => white phase
	soak.Start(time.Hour, base)

	if err := l.step(base); err != nil {
		t.Fatal(err)
	}
	assertUniformFrame(t, fl, 0xFF) // packed white: 0xF<<4|0xF

	if err := l.step(base.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	assertUniformFrame(t, fl, 0x00) // packed black
}

// assertUniformFrame checks the last flushed frame is full-size and every
// packed byte equals want.
func assertUniformFrame(t *testing.T, fl *fakeFlusher, want byte) {
	t.Helper()
	fl.mu.Lock()
	frame := append([]byte(nil), fl.lastFrame...)
	fl.mu.Unlock()
	if len(frame) != board.W*board.H/2 {
		t.Fatalf("frame is %d bytes, want %d", len(frame), board.W*board.H/2)
	}
	for i, b := range frame {
		if b != want {
			t.Fatalf("frame[%d] = %#x, want %#x", i, b, want)
		}
	}
}

func TestLoopSoakForcesFullContrastThenScheduleResumes(t *testing.T) {
	// Powersaving window is 23:00–07:00 @ 32 (powersavingCfg). Soak must
	// force 0xFF even inside the window; after expiry the schedule value
	// must be re-applied.
	l, fl := newTestLoop(t, func() *board.Snapshot { return nil }, powersavingCfg())
	soak := &Soak{}
	l.UseSoak(soak)

	inWindow := time.Date(2026, 7, 7, 23, 30, 0, 0, time.UTC)
	soak.Start(time.Minute, inWindow)

	if err := l.step(inWindow); err != nil {
		t.Fatal(err)
	}
	fl.mu.Lock()
	got := append([]byte(nil), fl.contrasts...)
	fl.mu.Unlock()
	if len(got) == 0 || got[len(got)-1] != 0xFF {
		t.Fatalf("during soak: contrasts = %v, want last == 0xFF", got)
	}

	// Step again after expiry, still inside the powersave window.
	after := inWindow.Add(2 * time.Minute)
	if err := l.step(after); err != nil {
		t.Fatal(err)
	}
	fl.mu.Lock()
	got = append([]byte(nil), fl.contrasts...)
	fl.mu.Unlock()
	if got[len(got)-1] != 32 {
		t.Fatalf("after soak: contrasts = %v, want last == 32 (powersave)", got)
	}
}

func TestLoopSoakResumesSceneAfterCancel(t *testing.T) {
	l, fl := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())
	soak := &Soak{}
	l.UseSoak(soak)

	base := time.Unix(1_600_000_000, 0)
	soak.Start(time.Hour, base)
	if err := l.step(base); err != nil {
		t.Fatal(err)
	}
	soak.Cancel()
	if err := l.step(base.Add(40 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	// A scene frame (nil snapshot => initialising scene) is not uniform:
	// at least two distinct byte values appear.
	fl.mu.Lock()
	frame := append([]byte(nil), fl.lastFrame...)
	fl.mu.Unlock()
	first := frame[0]
	uniform := true
	for _, b := range frame {
		if b != first {
			uniform = false
			break
		}
	}
	if uniform {
		t.Fatal("after cancel: frame is still uniform — scene rendering did not resume")
	}
}

// TestStepCallsBeatOnEveryTick verifies SetBeat's callback fires once per
// step() call, and that a nil beat (never set) never panics — exercised
// implicitly by every other test in this file.
func TestStepCallsBeatOnEveryTick(t *testing.T) {
	l, _ := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())
	var mu sync.Mutex
	beats := 0
	l.SetBeat(func() {
		mu.Lock()
		beats++
		mu.Unlock()
	})
	day := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := l.step(day.Add(time.Duration(i) * TickInterval)); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if beats != 3 {
		t.Fatalf("beat count = %d, want 3", beats)
	}
}

func TestUpdateHintOverlay(t *testing.T) {
	l, _ := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())

	hint := false
	l.SetUpdateHint(func() bool { return hint })

	day := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	// One frame without the hint: bottom-left pixels stay 0.
	if err := l.step(day); err != nil {
		t.Fatal(err)
	}
	if got := l.fb.At(0, l.fb.H-1); got != 0 {
		t.Fatalf("pixel (0,H-1) = %d before hint, want 0", got)
	}

	// Enable the hint: the 2x2 bottom-left block lights at level 6.
	hint = true
	if err := l.step(day.Add(TickInterval)); err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]int{{0, l.fb.H - 1}, {1, l.fb.H - 1}, {0, l.fb.H - 2}, {1, l.fb.H - 2}} {
		if got := l.fb.At(p[0], p[1]); got != updateHintLevel {
			t.Errorf("pixel (%d,%d) = %d with hint, want %d", p[0], p[1], got, updateHintLevel)
		}
	}
}

// TestOnFirstFrameFiresOnce verifies SetOnFirstFrame's callback fires
// exactly once, on the first successful flush, even across further ticks.
func TestOnFirstFrameFiresOnce(t *testing.T) {
	l, _ := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())

	fired := 0
	l.SetOnFirstFrame(func() { fired++ })
	day := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := l.step(day.Add(time.Duration(i) * TickInterval)); err != nil {
			t.Fatal(err)
		}
	}
	if fired != 1 {
		t.Errorf("OnFirstFrame fired %d times, want exactly 1", fired)
	}
}

func TestLoopNilSoakUnchanged(t *testing.T) {
	// A loop with no UseSoak call behaves exactly as before.
	l, fl := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())
	if err := l.step(time.Unix(1_600_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if n, _ := fl.stats(); n != 1 {
		t.Fatalf("flushes = %d, want 1", n)
	}
}
