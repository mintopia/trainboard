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
	writeFile func(path string, data []byte) error
	sleep     func(time.Duration)

	sta STAConfig
	ap  APConfig
}

var _ apDriver = (*mode2Driver)(nil)

// newMode2Driver builds the mode2 driver. writeFile and sleep default to
// os.WriteFile and time.Sleep in production; tests inject fakes.
func newMode2Driver(r Runner, iface string, writeFile func(string, []byte) error, sleep func(time.Duration)) *mode2Driver {
	if writeFile == nil {
		writeFile = func(path string, data []byte) error {
			return os.WriteFile(path, data, 0o600)
		}
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	return &mode2Driver{r: r, iface: iface, writeFile: writeFile, sleep: sleep}
}

// renderConf formats the wpa_supplicant conf with both network blocks. The
// conf format has no escaping for quoted strings, so any value containing a
// `"` is rejected outright rather than risking config injection.
func renderConf(sta STAConfig, ap APConfig) (string, error) {
	for _, v := range []string{sta.SSID, sta.PSK, ap.SSID, ap.Password} {
		if strings.Contains(v, `"`) {
			return "", fmt.Errorf("net: mode2: value contains disallowed quote character")
		}
	}
	return fmt.Sprintf(`ctrl_interface=/run/wpa_supplicant
country=GB
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
    key_mgmt=WPA-PSK
    psk="%s"
    disabled=1
}
`, sta.SSID, sta.PSK, ap.SSID, ap.Password), nil
}

// writeConf renders the current sta/ap state and writes it to wpaConfPath.
func (d *mode2Driver) writeConf() error {
	body, err := renderConf(d.sta, d.ap)
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
func (d *mode2Driver) ensureDaemon(ctx context.Context) error {
	if _, err := d.r.Run(ctx, "wpa_cli", "-i", d.iface, "status"); err != nil {
		if _, err := d.r.Run(ctx, "wpa_supplicant", "-B", "-i", d.iface, "-c", wpaConfPath); err != nil {
			return fmt.Errorf("net: mode2: start wpa_supplicant: %w", err)
		}
		return nil
	}
	if _, err := d.r.Run(ctx, "wpa_cli", "-i", d.iface, "reconfigure"); err != nil {
		return fmt.Errorf("net: mode2: reconfigure: %w", err)
	}
	return nil
}

// StartAP writes the conf (retaining whatever STA credentials are already
// known), ensures the daemon is running with it, selects the AP network,
// waits for it to beacon, then assigns the AP's static address.
func (d *mode2Driver) StartAP(ctx context.Context, ap APConfig) error {
	d.ap = ap
	if err := d.writeConf(); err != nil {
		return fmt.Errorf("net: mode2: StartAP: %w", err)
	}
	if err := d.ensureDaemon(ctx); err != nil {
		return fmt.Errorf("net: mode2: StartAP: %w", err)
	}
	if _, err := d.r.Run(ctx, "wpa_cli", "-i", d.iface, "select_network", "1"); err != nil {
		return fmt.Errorf("net: mode2: StartAP: select_network 1: %w", err)
	}
	if err := pollStatus(ctx, d.r, d.iface, d.sleep, func(kv map[string]string) bool {
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
// wait for association, then run a one-shot dhclient. It does not evaluate
// connectivity beyond that — the layered Check owns that.
func (d *mode2Driver) AttemptSTA(ctx context.Context, sta STAConfig) error {
	d.sta = sta
	if err := staAttempt(ctx, d.r, d.iface, sta, d.writeFile, d.sleep); err != nil {
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
