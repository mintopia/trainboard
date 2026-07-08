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

	"github.com/mintopia/trainboard/internal/board"
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
	//
	// Beat is called once per Run loop iteration, plus internally by
	// waitAPFallback every 30s while merely waiting in AP fallback, plus once
	// by runAPWait immediately before its single in-process AP-restore retry
	// (issue #48) — so the binding case is the worst-case gap around a single
	// STARetry-phase iteration (runAPWait, once waitAPFallback's budget is
	// up): the final leftover leg of that 30s heartbeat cadence (<=30s) is
	// immediately followed by StopAP (~5s) + Dnsmasq.Stop (~5s) + a toSTA
	// attempt (Prereqs ~5s + the enforced staAttemptBound of 45s) + a toAP
	// restore that may retry once internally (~40s with the issue #48
	// post-daemon-start AP poll budget) — none of Beat again until the
	// pre-retry beat (or the loop top):
	//   30 (wait remainder) + 5 (StopAP) + 5 (Dnsmasq.Stop)
	//   + 5 (Prereqs) + 45 (staAttemptBound) + 40 (toAP restore w/ retry)
	//   = 130s worst case.
	// The in-process AP-restore retry's own leg (teardown ~10s + another
	// ~40s toAP = ~50s) starts from that fresh pre-retry beat, so it never
	// binds. 90s would already be exceeded by the main chain alone, so this
	// is 150s (~20s margin over the 130s worst case) rather than adding yet
	// another mid-chain beat.
	managerBeatDeadline = 150 * time.Second
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

// wifiCountry returns the regulatory country to configure the radio with:
// the config's own value when set, defaulting to "GB" (matching
// config.Default() and config.WifiConfig.Country's documented consumer-side
// default) for a config predating this field or one that cleared it.
func wifiCountry(cfg config.Config) string {
	if cfg.Wifi.Country == "" {
		return "GB"
	}
	return cfg.Wifi.Country
}

// staFromDisk returns a closure that reads config.LoadRaw(cfgPath) on EVERY
// call, extracting and returning its STA credentials (SSID and PSK). This
// enables the credential-handoff flow: portal saves new WiFi creds, then
// Manager calls STA() → fresh creds without a process restart. A read error
// returns a zero STAConfig (graceful fallback to AP mode).
func staFromDisk(cfgPath string) func() netconn.STAConfig {
	return func() netconn.STAConfig {
		raw, err := config.LoadRaw(cfgPath)
		if err != nil {
			return netconn.STAConfig{} // zero on read error; graceful fallback
		}
		return netconn.STAConfig{SSID: raw.Wifi.SSID, PSK: raw.Wifi.PSK}
	}
}

// resolveE04Config picks the config to feed startConnectivityManager from
// the E04 (config error) boot path, given the result of a tolerant
// config.LoadRaw(path) read. When the raw read succeeded (rawErr == nil) it
// is preferred over a virgin config.Default(), even though the document as
// a whole failed board Validate() (that's WHY we're in this boot path at
// all) — this is what lets a previously-configured device whose config
// merely fails board validation (e.g. a stale origin) keep its persisted
// Web.PasswordHash across E04 boots.
//
// When rawErr != nil, raw is the zero Config (LoadRaw returns Default() with
// a nil error for a missing file, so a non-nil error here means the file
// exists but isn't even valid JSON) and config.Default() is used instead:
// this is the genuinely "wholly fresh/unreadable device" case.
func resolveE04Config(raw config.Config, rawErr error) config.Config {
	if rawErr != nil {
		return config.Default()
	}
	return raw
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
func startConnectivityManager(ctx context.Context, cfg config.Config, log *slog.Logger, wd *obs.Watchdog, sta func() netconn.STAConfig, onOnline func()) *netconn.Manager {
	ap := netconn.APConfig{
		SSID: "Trainboard-" + macTail(readWlanMAC()),
		Addr: "192.168.4.1/24",
	}
	country := wifiCountry(cfg)
	runner := netconn.NewExecRunner()
	driver := netconn.NewMode2Driver(runner, wlanIface, country, nil, nil)
	check := netconn.NewCheck(runner, wlanIface, darwinProbeHost, captiveProbeURL, httpGetProbe)
	dnsmasq := netconn.NewDnsmasq(runner, writeFile)

	mgr := netconn.NewManager(netconn.ManagerDeps{
		Driver:  driver,
		Check:   check,
		Dnsmasq: dnsmasq,
		Runner:  runner,
		Prereqs: func(pctx context.Context) error {
			return netconn.CheckPrereqs(pctx, runner, country, os.ReadFile, writeFile, filepath.Glob)
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

// connManager is the slice of *netconn.Manager the web-seam adapter needs:
// the published Status snapshot plus the two run-loop nudges. An interface
// (rather than the concrete Manager) purely so tests can substitute a fake;
// production only ever passes the real Manager.
type connManager interface {
	Status() netconn.Status
	RetryNow()
	NoteProvisioning(now time.Time)
}

var _ connManager = (*netconn.Manager)(nil)

// webConnSeams carries the four connectivity funcs the web package exposes
// as seams (web.Sources.Hotspot/LastSTAError, web.Actions.WifiRetry/
// NoteProvisioning). The zero value — what both boot paths pass when
// --manage-network is off — leaves every field nil, which the web Service
// nil-tolerates by design (nil hotspot, empty error, no-op actions).
type webConnSeams struct {
	hotspot          func() *board.Hotspot
	lastSTAError     func() string
	wifiRetry        func()
	noteProvisioning func()
}

// newWebConnSeams adapts the connectivity manager to the web seams. The read
// seams go through m.Status() on every call (the manager republishes an
// immutable snapshot as it moves between STA and AP), keeping internal/web
// free of any internal/net dependency: net types are mapped here, in cmd, to
// *board.Hotspot and plain strings.
//
// noteProvisioning stamps NoteProvisioning with now(): the web handlers call
// it on portal HTTP activity, which suppresses the manager's periodic STA
// retry. HTTP activity from the AP subnet implies a DHCP lease — a conscious
// simplification of the spec's lease-AND-HTTP pair; a mere association
// without traffic still never suppresses (no HTTP request, no
// NoteProvisioning call).
// connFault adapts the connectivity manager to the fault seam the composite
// snapshot source consumes: the current failing layer (Status.Stage) and
// whether the STA Prereqs gate reported the radio blocked (Status.RadioBlocked).
// Mapping net types to plain strings/bools here keeps internal/runtime free of
// any internal/net dependency. Read through m.Status() on every call, matching
// the other seams (the manager republishes an immutable snapshot as it moves
// between STA and AP).
func connFault(m connManager) func() (string, bool) {
	return func() (string, bool) {
		s := m.Status()
		return string(s.Stage), s.RadioBlocked
	}
}

func newWebConnSeams(m connManager, now func() time.Time) webConnSeams {
	return webConnSeams{
		hotspot:          func() *board.Hotspot { return m.Status().Hotspot },
		lastSTAError:     func() string { return m.Status().LastSTAErr },
		wifiRetry:        m.RetryNow,
		noteProvisioning: func() { m.NoteProvisioning(now()) },
	}
}
