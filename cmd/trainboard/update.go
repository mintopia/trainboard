// M5 self-update wiring: builds the checker/applier seams from the device
// state, decides whether the updater is usable, and derives the health
// probe. Kept out of main.go to keep run() readable.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/mintopia/trainboard/internal/buildinfo"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/update"
	"github.com/mintopia/trainboard/internal/web"
)

// healthDeadline is how long after start the payload has to come healthy
// (first frame + web probe) before promotion is abandoned and the
// launcher's boot counter is left to converge (spec §2).
const healthDeadline = 60 * time.Second

// updaterSeams bundles the wired updater for main to hand to web/runtime.
type updaterSeams struct {
	checker   *update.Checker
	enabled   bool
	statePath string
}

// buildUpdater assembles the checker + applier. The updater is enabled
// only when this is a slot install (state file present) AND the keyring is
// non-empty (key ceremony done) — config validity plays no part: even in
// an E04/E07 fault loop the operator can still fix things from the
// recovery web UI, including manually applying a known-working update
// (spec §2), so a disabled updater there would take away the one recovery
// path that doesn't need physical access. What a fault loop actually does
// is never start the checker's background Run loop (see runFaultLoop), not
// disable the updater seam itself. A disabled updater is still non-nil:
// every seam stays callable and reports "unavailable" gracefully.
func buildUpdater(cfg config.Config, slotsDir, statePath string, log *slog.Logger) *updaterSeams {
	keys, keyErr := update.Keyring()
	_, stateErr := update.LoadState(statePath)
	enabled := keyErr == nil && stateErr == nil
	if keyErr != nil {
		log.Info("self-update unavailable: keyring", "reason", keyErr.Error())
	}
	if stateErr != nil {
		log.Info("self-update unavailable: not a slot install", "reason", stateErr.Error())
	}
	applier := &update.Applier{
		SlotsDir:  slotsDir,
		StatePath: statePath,
		Running:   buildinfo.Version(),
		Keys:      keys,
		HTTP:      &http.Client{Timeout: 5 * time.Minute}, // binary download on Pi WiFi
		Log:       log,
	}
	checker := update.NewChecker(update.NewClient(), applier, cfg, enabled, log)
	return &updaterSeams{checker: checker, enabled: enabled, statePath: statePath}
}

// webSources returns the Sources/Actions fragments main merges into the web
// service wiring.
func (u *updaterSeams) webSources() func() update.Status { return u.checker.Status }

func (u *updaterSeams) webActions() (check, apply func(ctx context.Context) error, dismiss func() error) {
	return u.checker.CheckNow, u.checker.ApplyNow, func() error { return update.DismissRollback(u.statePath) }
}

// updateAvailable is the render loop's hint probe. It calls Checker.Available
// rather than Status: the render loop probes this at frame rate (~25fps),
// and Status's LoadState disk read + JSON parse is not something to pay on
// every frame.
func (u *updaterSeams) updateAvailable() bool { return u.checker.Available() }

// probeURL derives the loopback health-probe URL from the web listen
// address: an empty or wildcard host becomes 127.0.0.1. /login is the
// cheapest always-reachable authed-or-not route.
func probeURL(httpAddr string) string {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		host, port = "127.0.0.1", "80"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s/login", net.JoinHostPort(host, port))
}

// webProbe GETs probeURL and reports healthy for any HTTP response with a
// non-5xx status (the server is up and routing; auth state is irrelevant).
func webProbe(url string) func(ctx context.Context) error {
	client := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 {
			return fmt.Errorf("web probe: %s", resp.Status)
		}
		return nil
	}
}

// mergeUpdateSeams copies the updater's seams onto the web Sources/Actions
// structs (used by both boot paths).
func mergeUpdateSeams(src *web.Sources, act *web.Actions, u *updaterSeams) {
	src.UpdateStatus = u.webSources()
	check, apply, dismiss := u.webActions()
	act.UpdateCheck = check
	act.UpdateApply = apply
	act.UpdateDismiss = dismiss
}
