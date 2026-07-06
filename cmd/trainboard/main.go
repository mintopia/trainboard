// Command trainboard runs the departure board: config → Darwin poller →
// scene render loop → SSD1322 (or a PNG preview on host).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/buildinfo"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/display"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/runtime"
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
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Name(), buildinfo.Version())
		return nil
	}

	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(os.Stderr, ring, slog.LevelInfo)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fonts, err := board.LoadFonts()
	if err != nil {
		return err
	}

	var fl runtime.Flusher
	if *production {
		tr, err := display.OpenPeriph(display.PeriphConfig{SPIPort: "SPI0.0", DCPin: "GPIO25", ResetPin: "GPIO27", MaxHz: 16_000_000})
		if err != nil {
			return err
		}
		defer func() { _ = tr.Close() }()
		panel := display.New(tr)
		if err := panel.Init(); err != nil {
			return err
		}
		fl = panel
	} else {
		if err := os.MkdirAll(*previewDir, 0o755); err != nil {
			return err
		}
		fl = newPreviewSink(*previewDir, 25)
		log.Info("preview mode", "dir", *previewDir)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		// Config unusable: show the E04 fault on-screen and idle; the operator
		// fixes the file (M2 will offer a UI). systemd keeps us alive.
		log.Error("config load failed", "err", err.Error(), "path", *cfgPath)
		snap := &board.Snapshot{State: board.StateError, Fault: obs.FaultConfigError}
		loop := runtime.NewLoop(func() *board.Snapshot { return snap }, fl, config.Default(), fonts, buildinfo.Version(), log)
		return loop.Run(ctx)
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

	loop := runtime.NewLoop(poller.Snapshot, fl, cfg, fonts, buildinfo.Version(), log)
	log.Info("starting render loop", "version", buildinfo.Version())
	return loop.Run(ctx)
}
