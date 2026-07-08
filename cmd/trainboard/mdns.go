// mDNS wiring for --mdns (Task 4). The responder is started in both boot
// paths (run() and runConfigErrorLoop()) independent of --manage-network:
// mDNS discovery is a software-only feature that must keep working on a
// device that has never touched the connectivity manager. Kept in its own
// file, mirroring connectivity.go's split for startConnectivityManager.
package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/mintopia/trainboard/internal/mdns"
	netconn "github.com/mintopia/trainboard/internal/net"
	"github.com/mintopia/trainboard/internal/obs"
)

// mdnsBeatDeadline is the watchdog liveness window for the mDNS responder's
// poll loop (Task 4 wiring rules): generous relative to the responder's own
// internal 5s poll tick, matching the wide margins the other components'
// deadlines carry (renderBeatDeadline, managerBeatDeadline).
const mdnsBeatDeadline = 90 * time.Second

// mdnsSuppressor adapts mgr into the mdns.Config.SuppressWlan0 seam. A nil
// mgr (--manage-network off) yields a nil func — the responder's own
// documented "never suppress" default — rather than a func that always
// reports false: those two read identically from inside the responder today,
// but are different decisions, and only one of them is actually true when no
// manager exists to ask. A non-nil mgr suppresses wlan0 advertising exactly
// while the AP is up (Status().Hotspot != nil): the hotspot's own dnsmasq
// already answers on that interface, and the mDNS responder joining it too
// would just contend for the same broadcast domain.
func mdnsSuppressor(mgr *netconn.Manager) func() bool {
	if mgr == nil {
		return nil
	}
	return func() bool { return mgr.Status().Hotspot != nil }
}

// mdnsHostName mirrors mdns.NewZone's own hostname construction (lowercased
// "trainboard-<suffix>.local"), so anything displaying this string — today
// just the status page — shows exactly the name the responder answers for.
func mdnsHostName(suffix string) string {
	return "trainboard-" + strings.ToLower(suffix) + ".local"
}

// buildMDNSConfig assembles the mdns.Config startMDNS runs: Suffix derived
// from wlan0's MAC tail (matching the AP SSID's own
// macTail(readWlanMAC()) derivation in startConnectivityManager),
// SuppressWlan0 nil-safe over mgr, and Beat registered on wd. Split out from
// startMDNS so tests can inspect the constructed Config without opening real
// multicast sockets.
func buildMDNSConfig(mgr *netconn.Manager, wd *obs.Watchdog, log *slog.Logger) mdns.Config {
	return mdns.Config{
		Suffix:        strings.ToUpper(macTail(readWlanMAC())),
		SuppressWlan0: mdnsSuppressor(mgr),
		Beat:          wd.Register("mdns", mdnsBeatDeadline),
		Log:           log,
	}
}

// startMDNS launches the mDNS responder for the life of ctx. mgr may be nil
// (--manage-network off): SuppressWlan0 is then nil too, so the responder
// simply never suppresses wlan0. Exit is logged but the beat is deliberately
// NOT re-registered afterwards — matching startConnectivityManager's
// precedent — so a dead responder goes stale and the watchdog escalates via
// a full reboot rather than software attempting its own recovery.
func startMDNS(ctx context.Context, mgr *netconn.Manager, wd *obs.Watchdog, log *slog.Logger) {
	cfg := buildMDNSConfig(mgr, wd, log)
	go func() {
		if err := mdns.Run(ctx, cfg); err != nil {
			log.Error("mdns responder exited", "err", err.Error())
		}
	}()
}

// mdnsStateFunc builds the web.Sources.MDNSState closure: nil when mDNS is
// disabled (--mdns=false) — the web Service reads a nil source as "" (feature
// off) — otherwise a closure over the static hostname derived from wlan0's
// MAC tail at boot (matching buildMDNSConfig's own Suffix derivation).
// Per-interface live state is deliberately NOT surfaced here: that's YAGNI
// for a status page whose job is just telling the operator the feature is on
// and what name to expect, and the log already carries per-interface
// add/remove detail.
func mdnsStateFunc(enabled bool) func() string {
	if !enabled {
		return nil
	}
	host := mdnsHostName(strings.ToUpper(macTail(readWlanMAC())))
	return func() string { return host }
}
