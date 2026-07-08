package net

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// wpaConfPath is the single conf file the mode2 driver owns; it holds both
// the STA and AP network blocks (only one is ever "selected" at a time).
const wpaConfPath = "/run/trainboard-wpa.conf"

// mode2Driver drives a single wpa_supplicant instance in "mode=2" (native
// AP) mode: one conf file with a disabled STA network (id 0) and a disabled
// AP network (id 1), switched between via select_network.
type mode2Driver struct {
	r         Runner
	iface     string
	country   string
	writeFile func(path string, data []byte) error
	sleep     func(time.Duration)

	sta STAConfig
	ap  APConfig
}

var _ Driver = (*mode2Driver)(nil)

// NewMode2Driver builds the production mode2 driver (cmd/trainboard, Task
// 12): a single wpa_supplicant instance driving both STA and AP via
// mode=2, evaluated as the M3a default pending the M3b hardware matrix
// (spec/ADR 0003 addendum). country is the regulatory domain rendered into
// the conf (defaulted to "GB" by the caller when config.Wifi.Country is
// unset). writeFile and sleep default to os.WriteFile and time.Sleep in
// production; pass nil for both to get those defaults, or inject fakes (as
// the internal constructor's tests do).
func NewMode2Driver(r Runner, iface, country string, writeFile func(string, []byte) error, sleep func(time.Duration)) Driver {
	return newMode2Driver(r, iface, country, writeFile, sleep)
}

// newMode2Driver builds the mode2 driver. writeFile and sleep default to
// os.WriteFile and time.Sleep in production; tests inject fakes.
func newMode2Driver(r Runner, iface, country string, writeFile func(string, []byte) error, sleep func(time.Duration)) *mode2Driver {
	if writeFile == nil {
		writeFile = func(path string, data []byte) error {
			return os.WriteFile(path, data, 0o600)
		}
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	return &mode2Driver{r: r, iface: iface, country: country, writeFile: writeFile, sleep: sleep}
}

// renderConf formats the wpa_supplicant conf with both network blocks. The
// conf format has no escaping for quoted strings, so any value containing a
// `"` is rejected outright rather than risking config injection. country is
// the regulatory domain to render (see NewMode2Driver).
func renderConf(sta STAConfig, ap APConfig, country string) (string, error) {
	for _, v := range []string{sta.SSID, sta.PSK, ap.SSID} {
		if strings.Contains(v, `"`) {
			return "", fmt.Errorf("net: mode2: value contains disallowed quote character")
		}
	}
	// The AP block is open (key_mgmt=NONE, no psk): the setup hotspot carries
	// no WPA2 password (issue #44 — operator decision, risk accepted).
	return fmt.Sprintf(`ctrl_interface=/run/wpa_supplicant
country=%s
network={
    id_str="sta"
    ssid="%s"
    psk="%s"
    disabled=1
}
network={
    id_str="ap"
    ssid="%s"
    mode=2
    frequency=2437
    key_mgmt=NONE
    disabled=1
}
`, country, sta.SSID, sta.PSK, ap.SSID), nil
}

// writeConf renders the current sta/ap state and writes it to wpaConfPath.
func (d *mode2Driver) writeConf() error {
	body, err := renderConf(d.sta, d.ap, d.country)
	if err != nil {
		return fmt.Errorf("net: mode2: render conf: %w", err)
	}
	if err := d.writeFile(wpaConfPath, []byte(body)); err != nil {
		return fmt.Errorf("net: mode2: write conf: %w", err)
	}
	return nil
}

// ensureDaemon starts wpa_supplicant if wpa_cli status errors (not running);
// otherwise it tells the running daemon to pick up the conf we just wrote.
//
// started is true iff this call spawned wpa_supplicant -B (the cold-start
// branch), which the caller uses to budget the subsequent bring-up.
//
// After a spawn, ensureDaemon polls `wpa_cli status` (up to pollAttempts,
// pollInterval apart, via the injected sleeper — the same seam pollStatus
// uses) until the control socket answers, i.e. until the command exits 0. The
// content of the status is irrelevant here (want always true); we only need
// the socket to be accepting commands. Without this, the immediately-following
// select_network / association races the daemon's socket coming up, which on
// real hardware fails the first STA attempt outright and costs a ~5-minute AP
// detour (issue #47).
func (d *mode2Driver) ensureDaemon(ctx context.Context) (started bool, err error) {
	if _, err := d.r.Run(ctx, "wpa_cli", "-i", d.iface, "status"); err == nil {
		// Already running: tell it to reload the conf we just wrote.
		if _, err := d.r.Run(ctx, "wpa_cli", "-i", d.iface, "reconfigure"); err != nil {
			return false, fmt.Errorf("net: mode2: reconfigure: %w", err)
		}
		return false, nil
	}
	if _, err := d.r.Run(ctx, "wpa_supplicant", "-B", "-i", d.iface, "-c", wpaConfPath); err != nil {
		return false, fmt.Errorf("net: mode2: start wpa_supplicant: %w", err)
	}
	if err := pollStatus(ctx, d.r, d.iface, d.sleep, pollAttempts, func(map[string]string) bool { return true }, "daemon ctrl socket not ready"); err != nil {
		return true, fmt.Errorf("net: mode2: %w", err)
	}
	return true, nil
}

// apPollsAfterDaemonStart is StartAP's AP-active poll budget when ensureDaemon
// just spawned wpa_supplicant (issue #48): a cold daemon (e.g. the previous
// instance was SIGKILL'd) has to initialise the driver AND bring the AP up
// inside this window, which on Pi Zero W 2 hardware misses the default
// pollAttempts budget (10 x 500ms = 5s). 20 x 500ms = 10s covers the observed
// cold bring-up with margin; the already-running branch keeps pollAttempts.
const apPollsAfterDaemonStart = 20

// StartAP writes the conf (retaining whatever STA credentials are already
// known), ensures the daemon is running with it, selects the AP network
// (which leaves STA — killing any dhclient daemon still renewing that
// lease, issue #46), waits for it to beacon, then assigns the AP's static
// address. When ensureDaemon spawned the daemon this call, the AP-active
// wait gets the extended apPollsAfterDaemonStart budget (issue #48).
func (d *mode2Driver) StartAP(ctx context.Context, ap APConfig) error {
	d.ap = ap
	if err := d.writeConf(); err != nil {
		return fmt.Errorf("net: mode2: StartAP: %w", err)
	}
	started, err := d.ensureDaemon(ctx)
	if err != nil {
		return fmt.Errorf("net: mode2: StartAP: %w", err)
	}
	if _, err := d.r.Run(ctx, "wpa_cli", "-i", d.iface, "select_network", "1"); err != nil {
		return fmt.Errorf("net: mode2: StartAP: select_network 1: %w", err)
	}
	// select_network 1 disables network 0 (STA) as a side effect — this is
	// the point this driver leaves STA for AP (issue #46), so any dhclient
	// daemon staAttempt left renewing the STA lease must die here.
	killDHClient(ctx, d.r, nil)
	apPolls := pollAttempts
	if started {
		apPolls = apPollsAfterDaemonStart
	}
	if err := pollStatus(ctx, d.r, d.iface, d.sleep, apPolls, func(kv map[string]string) bool {
		return kv["wpa_state"] == "COMPLETED" && kv["mode"] == "AP"
	}, "AP not active"); err != nil {
		return fmt.Errorf("net: mode2: StartAP: %w", err)
	}
	if _, err := d.r.Run(ctx, "ip", "addr", "flush", "dev", d.iface); err != nil {
		return fmt.Errorf("net: mode2: StartAP: ip addr flush: %w", err)
	}
	if _, err := d.r.Run(ctx, "ip", "addr", "add", ap.Addr, "dev", d.iface); err != nil {
		return fmt.Errorf("net: mode2: StartAP: ip addr add: %w", err)
	}
	return nil
}

// StopAP disables the AP network and flushes the interface's address. It
// does not start the STA network — that is AttemptSTA's job.
func (d *mode2Driver) StopAP(ctx context.Context) error {
	if _, err := d.r.Run(ctx, "wpa_cli", "-i", d.iface, "disable_network", "1"); err != nil {
		return fmt.Errorf("net: mode2: StopAP: disable_network 1: %w", err)
	}
	if _, err := d.r.Run(ctx, "ip", "addr", "flush", "dev", d.iface); err != nil {
		return fmt.Errorf("net: mode2: StopAP: ip addr flush: %w", err)
	}
	return nil
}

// AttemptSTA switches to the client network using the shared staAttempt
// flow: reconfigure the (already-running) daemon, select the STA network,
// wait for association, then run a one-shot dhclient. The conf staAttempt
// writes retains d.ap's (disabled) block alongside the STA block — mode2's
// single conf file always holds both networks (see the package doc comment
// above), so a reconfigure here must not drop the AP one out from under a
// later StartAP. It does not evaluate connectivity beyond that — the
// layered Check owns that.
func (d *mode2Driver) AttemptSTA(ctx context.Context, sta STAConfig) error {
	d.sta = sta
	render := func(s STAConfig) ([]byte, error) {
		body, err := renderConf(s, d.ap, d.country)
		return []byte(body), err
	}
	if err := staAttempt(ctx, d.r, d.iface, sta, render, d.writeFile, d.sleep); err != nil {
		return fmt.Errorf("net: mode2: AttemptSTA: %w", err)
	}
	return nil
}

// APActive reports whether wpa_supplicant currently reports a beaconing AP.
func (d *mode2Driver) APActive(ctx context.Context) (bool, error) {
	out, err := d.r.Run(ctx, "wpa_cli", "-i", d.iface, "status")
	if err != nil {
		return false, fmt.Errorf("net: mode2: APActive: %w", err)
	}
	kv := parseWpaStatus(out)
	return kv["wpa_state"] == "COMPLETED" && kv["mode"] == "AP", nil
}
