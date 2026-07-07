package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/render"
)

// TickInterval is the fixed frame period: 0.04s, 25fps, reference parity.
const TickInterval = 40 * time.Millisecond

// timingEveryTicks spaces "frame timing" ring events ~15s apart so the
// 256-entry ring keeps history instead of being flooded at 25fps.
const timingEveryTicks = 375

// Flusher is the panel seam: *display.SSD1322 in production, the PNG
// preview transport on host, a fake in tests.
type Flusher interface {
	Flush(packed []byte) error
	SetContrast(level byte) error
}

// Loop renders the active scene at a fixed rate, full-frame flushing every
// tick (ADR 0002 baseline). It owns the frame tick counter, which restarts
// whenever a new snapshot is published so scene entry animations replay.
type Loop struct {
	src     func() *board.Snapshot
	fl      Flusher
	cfg     config.Config
	fonts   *board.Fonts
	version string
	log     *slog.Logger

	fb         *render.Framebuffer
	scene      *render.Scene
	last       *board.Snapshot
	tick       int
	brightness int  // last applied; -1 = never
	flushed    bool // first-frame logged
	sceneBuilt bool
	soak       *Soak // optional soak override; nil = feature not wired
	soaking    bool  // previous tick was a soak frame (drives exit cleanup)
	beat       func()
}

// NewLoop wires a snapshot source (Poller.Snapshot) to a Flusher.
func NewLoop(src func() *board.Snapshot, fl Flusher, cfg config.Config, fonts *board.Fonts, version string, log *slog.Logger) *Loop {
	return &Loop{src: src, fl: fl, cfg: cfg, fonts: fonts, version: version, log: log, fb: render.New(board.W, board.H), brightness: -1}
}

// UseSoak attaches the soak override. Call before Run; the loop reads it on
// every tick. A nil receiver-field (never attached) disables the feature.
func (l *Loop) UseSoak(s *Soak) { l.soak = s }

// SetBeat installs a heartbeat callback invoked once per rendered tick
// (called from step). nil (the default) disables the hook.
func (l *Loop) SetBeat(f func()) { l.beat = f }

// Run ticks until ctx cancels. A flush error is returned (fatal: the panel
// is unreachable; systemd restarts the unit).
func (l *Loop) Run(ctx context.Context) error {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			if err := l.step(now); err != nil {
				return err
			}
		}
	}
}

// step renders and flushes exactly one frame at the given instant.
func (l *Loop) step(now time.Time) error {
	if l.beat != nil {
		l.beat()
	}
	if l.soak != nil && l.soak.Remaining(now) > 0 {
		return l.soakStep(now)
	}
	if l.soaking {
		// Soak just ended (expired or cancelled): force the powersave
		// schedule's contrast to re-apply on this tick, then resume the
		// existing scene where it left off.
		l.soaking = false
		l.brightness = -1
	}

	if snap := l.src(); snap != l.last || !l.sceneBuilt {
		l.scene = board.BuildScene(snap, l.cfg.Layout, l.version, l.fonts)
		l.last = snap
		l.tick = 0
		l.sceneBuilt = true
		l.log.Debug("scene swapped")
	}

	if b := l.cfg.BrightnessAt(now); b != l.brightness {
		if err := l.fl.SetContrast(byte(b)); err != nil {
			return err
		}
		l.brightness = b
	}

	l.fb.Clear()
	renderStart := time.Now()
	l.scene.Render(l.fb, l.tick, now)
	packed := l.fb.Pack()
	renderDur := time.Since(renderStart)
	flushStart := time.Now()
	if err := l.fl.Flush(packed); err != nil {
		return err
	}
	flushDur := time.Since(flushStart)
	if !l.flushed {
		l.flushed = true
		l.log.Info("first frame flushed", "render_us", renderDur.Microseconds(), "flush_us", flushDur.Microseconds())
	}
	if l.tick > 0 && l.tick%timingEveryTicks == 0 {
		l.log.Info("frame timing", "render_us", renderDur.Microseconds(), "flush_us", flushDur.Microseconds())
	}
	l.tick++
	return nil
}

// soakStep renders one burn-in soak frame: the whole panel full-white or
// full-black on a 2-second wall-clock phase, at full contrast. No scene, no
// overlay — any static element during soak would defeat the treatment.
func (l *Loop) soakStep(now time.Time) error {
	l.soaking = true
	if l.brightness != 0xFF {
		if err := l.fl.SetContrast(0xFF); err != nil {
			return err
		}
		l.brightness = 0xFF
	}
	level := byte(0x00)
	if now.Unix()/2%2 == 0 {
		level = 0x0F
	}
	for i := range l.fb.Pix {
		l.fb.Pix[i] = level
	}
	return l.fl.Flush(l.fb.Pack())
}
