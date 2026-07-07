// Command trainboard runs the departure board: config → Darwin poller →
// scene render loop → SSD1322 (or a PNG preview on host).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/buildinfo"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/display"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/runtime"
	"github.com/mintopia/trainboard/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "trainboard:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", config.DefaultPath, "config file path")
	production := flag.Bool("production", false, "drive the real SSD1322 over SPI")
	previewDir := flag.String("preview-dir", "./preview", "PNG preview directory (host mode)")
	fixture := flag.String("fixture", "", "JSON board fixture instead of live Darwin (dev)")
	httpAddr := flag.String("http", ":80", "address for the embedded config/status web server")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Name(), buildinfo.Version())
		return nil
	}

	startedAt := time.Now()
	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(os.Stderr, ring, slog.LevelInfo)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	soak := &runtime.Soak{} // shared burn-in soak state: web starts/cancels, loop renders

	fonts, err := board.LoadFonts()
	if err != nil {
		return err
	}

	var fl runtime.Flusher
	var previewLatest func() []byte
	if *production {
		// DC/RST match the panel's physical wiring, which follows luma.core's
		// spi() defaults (gpio_DC=24, gpio_RST=25) — the reference Python
		// project constructed spi() with no pin args (reference/src/trains/
		// board.py). Verified on hardware 2026-07-07: the previous GPIO25/27
		// assignment toggled the panel's real RST line as D/C, so the panel
		// was reset mid-init and never displayed anything.
		tr, err := display.OpenPeriph(display.PeriphConfig{SPIPort: "SPI0.0", DCPin: "GPIO24", ResetPin: "GPIO25", MaxHz: 16_000_000})
		if err != nil {
			return err
		}
		defer func() { _ = tr.Close() }()
		panel := display.New(tr)
		if err := panel.Init(); err != nil {
			return err
		}
		// Production still serves a live preview to the web UI, but never to
		// disk: newPreviewSink("", 25) skips disk writes entirely (dir=="")
		// while still encoding for Latest(). teeFlusher fans every Flush and
		// SetContrast call out to both; the panel's error (not the
		// preview's) is what the render loop sees.
		sink := newPreviewSink("", 25)
		fl = newTeeFlusher(panel, sink, log)
		previewLatest = sink.Latest
	} else {
		if err := os.MkdirAll(*previewDir, 0o755); err != nil {
			return err
		}
		sink := newPreviewSink(*previewDir, 25)
		fl = sink
		previewLatest = sink.Latest
		log.Info("preview mode", "dir", *previewDir)
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		// Config unusable (missing/unparsable/invalid): show the E04 fault
		// on-screen and idle; the operator fixes the file via the embedded
		// web UI's /setup and /config, which stay reachable in this path
		// too. systemd keeps us alive.
		return runConfigErrorLoop(ctx, fl, fonts, log, *cfgPath, *httpAddr, ring, previewLatest, startedAt, soak, err)
	}
	log.Info("config loaded", "config", cfg.Redacted().String())

	var fetcher runtime.Fetcher
	if *fixture != "" {
		fetcher, err = newFixtureFetcher(*fixture)
		if err != nil {
			return err
		}
		log.Info("fixture mode", "path", *fixture)
	} else {
		fetcher = data.NewClient(cfg.Darwin.Token)
	}

	poller := runtime.NewPoller(fetcher, cfg, log)
	go poller.Run(ctx)

	startWebServer(ctx, *cfgPath, *httpAddr, poller.Snapshot, ring, previewLatest, startedAt, soak, log)

	loop := runtime.NewLoop(poller.Snapshot, fl, cfg, fonts, buildinfo.Version(), log)
	loop.UseSoak(soak)
	log.Info("starting render loop", "version", buildinfo.Version())
	return loop.Run(ctx)
}

// startWebServer builds the web.Service/Server over cfgPath and runs it in
// the background for the remainder of ctx's lifetime. It is shared by both
// boot paths (valid config and the E04 error loop) so a virgin or broken
// device always has /setup and /config reachable to fix itself.
//
// Apply is an immediate clean exit: the 500ms "let the response finish
// writing first" delay already lives in the web handlers (Task 8), so main's
// side of Apply must not add a second delay of its own. systemd is expected
// to restart the process. Reboot shells out to systemctl and reports the
// error rather than the web handler having to guess.
func startWebServer(ctx context.Context, cfgPath, httpAddr string, snapshot func() *board.Snapshot, ring *obs.Ring, previewLatest func() []byte, startedAt time.Time, soak *runtime.Soak, log *slog.Logger) {
	actions := web.Actions{
		Apply: func() {
			log.Info("applying config: exiting for restart")
			os.Exit(0)
		},
		Reboot: func() error {
			return exec.Command("systemctl", "reboot").Run()
		},
		SoakStart:  func(d time.Duration) { soak.Start(d, time.Now()) },
		SoakCancel: soak.Cancel,
	}
	sources := web.Sources{
		Snapshot:      snapshot,
		Ring:          ring,
		PreviewPNG:    previewLatest,
		Version:       buildinfo.Version(),
		StartedAt:     startedAt,
		SoakRemaining: func() time.Duration { return soak.Remaining(time.Now()) },
	}
	svc := web.NewService(cfgPath, sources, actions, log)
	srv := web.NewServer(svc, log)
	log.Info("starting web server", "addr", httpAddr)
	go func() {
		if err := srv.Run(ctx, httpAddr); err != nil {
			log.Error("web server exited", "error", err.Error())
		}
	}()
}

// loadConfig reads and fully validates the config at path, returning an
// error for both load failures (unreadable/unparsable file) and validation
// failures. config.Load's own doc warns that a missing file returns
// Default() with a nil error, and Default() is itself invalid (empty
// origin/token) — so loadConfig must Validate() on top of Load to catch the
// fresh-install case, matching NewPoller's documented precondition that cfg
// has passed Validate.
func loadConfig(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

// runConfigErrorLoop renders the E04 fault scene and idles forever (until
// ctx is cancelled): the shared fallback for both a Load error (unreadable
// file) and a Validate error (missing/invalid values, including the
// fresh-install case where Default() doesn't pass Validate). The web server
// still runs here, over the SAME config path, so a virgin or broken device
// can be fixed from /setup or /config without needing physical access.
func runConfigErrorLoop(ctx context.Context, fl runtime.Flusher, fonts *board.Fonts, log *slog.Logger, path, httpAddr string, ring *obs.Ring, previewLatest func() []byte, startedAt time.Time, soak *runtime.Soak, err error) error {
	log.Error("config error", "err", err.Error(), "path", path)
	snap := &board.Snapshot{State: board.StateError, Fault: obs.FaultConfigError}
	staticSnapshot := func() *board.Snapshot { return snap }

	startWebServer(ctx, path, httpAddr, staticSnapshot, ring, previewLatest, startedAt, soak, log)

	loop := runtime.NewLoop(staticSnapshot, fl, config.Default(), fonts, buildinfo.Version(), log)
	loop.UseSoak(soak)
	return loop.Run(ctx)
}
