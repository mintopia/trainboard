// Wiring for the connectivity Manager behind --manage-network (Task 12):
// production Runner/driver/check/dnsmasq/prereqs, the AP identity, and the
// watchdog beat registration. Kept in its own file so main.go's two boot
// paths (valid config, E04 error loop) can both call startConnectivityManager
// without duplicating the construction.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mintopia/trainboard/internal/config"
	netconn "github.com/mintopia/trainboard/internal/net"
	"github.com/mintopia/trainboard/internal/obs"
)

const (
	// wlanIface is the only radio this device drives (single onboard wifi
	// adapter; no interface discovery needed).
	wlanIface = "wlan0"
	// wlanMACPath is read once at connectivity-manager startup to build a
	// unique per-device AP SSID.
	wlanMACPath = "/sys/class/net/wlan0/address"
	// darwinProbeHost is the layered Check's DNS-layer target: resolving the
	// Darwin/OpenLDBWS host the board actually depends on is a more useful
	// signal than a generic well-known name (M3a plan §Task 4).
	darwinProbeHost = "lite.realtime.nationalrail.co.uk"
	// captiveProbeURL is Google's standard captive-portal probe: a 204 with
	// no body when the network is clean; a captive portal intercepts it
	// with its own (non-204) response.
	captiveProbeURL = "http://connectivitycheck.gstatic.com/generate_204"
	// managerBeatDeadline is the watchdog liveness window for the
	// connectivity manager's Run loop (Task 12 wiring rules).
	managerBeatDeadline = 90 * time.Second
	// httpProbeTimeout bounds the captive-portal probe request.
	httpProbeTimeout = 10 * time.Second
)

// writeFile adapts os.WriteFile to the func(string, []byte) error shape the
// net package's constructors expect, at the 0600 mode used throughout this
// package's other config/state files.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// readWlanMAC reads wlan0's MAC address from sysfs, used to build a unique
// per-device AP SSID (Trainboard-XXXX). Returns "" on any error (no wlan0,
// dev/host environment, permission) — macTail("") still returns a usable
// (if generic) suffix rather than failing the whole boot over a cosmetic
// detail.
func readWlanMAC() string {
	data, err := os.ReadFile(wlanMACPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// macTail returns the last two octets of a colon-separated MAC address,
// uppercased with the colons removed (e.g. "b8:27:eb:12:34:56" -> "3456"),
// for a short, human-distinguishable AP SSID suffix. Falls back to "0000"
// for anything shaped unexpectedly (including the empty string readWlanMAC
// returns on error).
func macTail(mac string) string {
	parts := strings.Split(mac, ":")
	if len(parts) < 2 {
		return "0000"
	}
	tail := parts[len(parts)-2] + parts[len(parts)-1]
	return strings.ToUpper(tail)
}

// resolveAPPassword returns the AP-mode password to advertise, generating
// and best-effort persisting one when the config doesn't have one yet: the
// M3 spec requires the AP to work even on a wholly unconfigured device
// (this is exercised from the E04 boot path), and an unconfigured device's
// config won't pass config.Save's full Validate() (missing origin/token),
// so the persist attempt uses the lighter SaveConnectivity/
// ValidateConnectivity tier instead — see the Task 12 report for the
// config.Save investigation this rests on. A persist failure (e.g.
// ValidateConnectivity also unmet, because no admin password is set yet
// either) is logged and tolerated: the freshly generated password is still
// used for THIS boot's AP (and shown on its on-screen hotspot scene
// regardless of disk persistence), just not carried over to the next boot
// until setup completes far enough for one of the two validation tiers to
// accept it.
func resolveAPPassword(cfg config.Config, cfgPath string, log *slog.Logger) string {
	if cfg.Provisioning.APPassword != "" {
		return cfg.Provisioning.APPassword
	}
	pw, err := config.GenerateAPPassword()
	if err != nil {
		log.Error("connectivity: generating AP password failed", "err", err.Error())
		return ""
	}
	next := cfg
	next.Provisioning.APPassword = pw
	if err := config.SaveConnectivity(cfgPath, next); err != nil {
		log.Warn("connectivity: could not persist generated AP password yet (will retry next boot)", "err", err.Error())
	}
	return pw
}

// httpGetProbe is the layered Check's captive-portal probe transport: a
// short-timeout GET with redirects left unfollowed (a captive portal's
// redirect response is itself the signal that trips the non-204 check;
// following it would just fetch the portal's real page for no benefit).
func httpGetProbe(ctx context.Context, url string) (int, string, error) {
	client := &http.Client{
		Timeout: httpProbeTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, "", nil
}

// startConnectivityManager builds and runs the connectivity Manager behind
// --manage-network: production Runner/driver/check/dnsmasq/prereqs, an AP
// identity derived from wlan0's MAC, the caller-supplied STA closure and
// online callback, and a watchdog beat registered on wd. The mode2 driver
// is the M3a default, pending the M3b hardware evaluation matrix (ADR 0003
// addendum) that decides between it and the hostapd fallback.
//
// It returns the Manager so the caller can compose Status().Hotspot into
// the render/web snapshot source (runtime.HotspotSnapshotSource).
func startConnectivityManager(ctx context.Context, cfg config.Config, cfgPath string, log *slog.Logger, wd *obs.Watchdog, sta func() netconn.STAConfig, onOnline func()) *netconn.Manager {
	ap := netconn.APConfig{
		SSID:     "Trainboard-" + macTail(readWlanMAC()),
		Password: resolveAPPassword(cfg, cfgPath, log),
		Addr:     "192.168.4.1/24",
	}
	runner := netconn.NewExecRunner()
	driver := netconn.NewMode2Driver(runner, wlanIface, nil, nil)
	check := netconn.NewCheck(runner, wlanIface, darwinProbeHost, captiveProbeURL, httpGetProbe)
	dnsmasq := netconn.NewDnsmasq(runner, writeFile)

	mgr := netconn.NewManager(netconn.ManagerDeps{
		Driver:  driver,
		Check:   check,
		Dnsmasq: dnsmasq,
		Prereqs: func(pctx context.Context) error {
			return netconn.CheckPrereqs(pctx, runner, os.ReadFile, writeFile, filepath.Glob)
		},
		AP:       ap,
		STA:      sta,
		OnOnline: onOnline,
		Beat:     wd.Register("manager", managerBeatDeadline),
		Log:      log,
		Now:      time.Now,
		After:    time.After,
	})

	go func() {
		if err := mgr.Run(ctx); err != nil {
			log.Error("connectivity manager exited", "err", err.Error())
			// Deliberately NOT re-registered: its watchdog beat goes stale
			// and systemd reboots — the escalation path (M3 spec
			// §Watchdog). Manager.Run's own doc is explicit that there is
			// no safe software recovery from "neither STA nor a verified AP
			// will come up", so letting the hardware watchdog force a full
			// reboot here is the intended outcome, not a bug.
		}
	}()

	return mgr
}
