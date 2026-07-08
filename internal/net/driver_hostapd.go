package net

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// hostapdConfPath is the config file the hostapd driver renders and hands to
// the hostapd binary.
const hostapdConfPath = "/run/trainboard-hostapd.conf"

// hostapdDriver is the fallback apDriver for hardware where mode2 (native
// AP mode inside a single wpa_supplicant instance) isn't supported: it runs
// a real hostapd binary for the AP, and defers to the (separately running)
// wpa_supplicant instance's STA network for the client side.
type hostapdDriver struct {
	r         Runner
	iface     string
	country   string
	writeFile func(path string, data []byte) error
	sleep     func(time.Duration)
}

var _ Driver = (*hostapdDriver)(nil)

// newHostapdDriver builds the hostapd driver. country is the regulatory
// domain rendered into both the AP conf (country_code=) and the STA-only
// conf staAttempt writes (country=), defaulted to "GB" by the caller when
// config.Wifi.Country is unset. writeFile and sleep default to os.WriteFile
// and time.Sleep in production; tests inject fakes.
func newHostapdDriver(r Runner, iface, country string, writeFile func(string, []byte) error, sleep func(time.Duration)) *hostapdDriver {
	if writeFile == nil {
		writeFile = func(path string, data []byte) error {
			return os.WriteFile(path, data, 0o600)
		}
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	return &hostapdDriver{r: r, iface: iface, country: country, writeFile: writeFile, sleep: sleep}
}

// renderHostapdConf formats the hostapd conf. hostapd.conf is a plain
// key=value file with no quoting, so a newline embedded in the SSID would let
// an attacker inject arbitrary directives; the SSID is also checked for `"`
// for consistency with the wpa_supplicant conf guard. Either character is
// rejected outright rather than risking config injection.
//
// The AP is open: no wpa/wpa_passphrase directives are emitted, so hostapd
// brings the setup hotspot up with no encryption (issue #44 — operator
// decision, risk accepted).
func (d *hostapdDriver) renderHostapdConf(ap APConfig) (string, error) {
	for _, v := range []string{ap.SSID} {
		if strings.ContainsAny(v, "\n\r\"") {
			return "", fmt.Errorf("net: hostapd: value contains disallowed newline or quote character")
		}
	}
	return fmt.Sprintf(`interface=%s
driver=nl80211
ssid=%s
country_code=%s
hw_mode=g
channel=6
`, d.iface, ap.SSID, d.country), nil
}

// StartAP releases the iface from wpa_supplicant's STA control (tolerating
// failure — the STA network may not exist or may not be running yet), kills
// any dhclient daemon still renewing the STA lease (issue #46), writes the
// hostapd conf, launches hostapd, then assigns the AP's static address.
func (d *hostapdDriver) StartAP(ctx context.Context, ap APConfig) error {
	_, _ = d.r.Run(ctx, "wpa_cli", "-i", d.iface, "disable_network", "0") // tolerated
	// disable_network 0 is this driver's "leave STA" verb (issue #46) — any
	// dhclient daemon staAttempt left renewing the STA lease must die here.
	killDHClient(ctx, d.r, nil)

	body, err := d.renderHostapdConf(ap)
	if err != nil {
		return fmt.Errorf("net: hostapd: StartAP: %w", err)
	}
	if err := d.writeFile(hostapdConfPath, []byte(body)); err != nil {
		return fmt.Errorf("net: hostapd: StartAP: write conf: %w", err)
	}
	if _, err := d.r.Run(ctx, "hostapd", "-B", hostapdConfPath); err != nil {
		return fmt.Errorf("net: hostapd: StartAP: %w", err)
	}
	if _, err := d.r.Run(ctx, "ip", "addr", "flush", "dev", d.iface); err != nil {
		return fmt.Errorf("net: hostapd: StartAP: ip addr flush: %w", err)
	}
	if _, err := d.r.Run(ctx, "ip", "addr", "add", ap.Addr, "dev", d.iface); err != nil {
		return fmt.Errorf("net: hostapd: StartAP: ip addr add: %w", err)
	}
	return nil
}

// StopAP kills hostapd (tolerating "no matching process", pkill's exit 1)
// and flushes the interface's address. It does not start the STA network —
// that is AttemptSTA's job.
func (d *hostapdDriver) StopAP(ctx context.Context) error {
	_, _ = d.r.Run(ctx, "pkill", "-x", "hostapd") // tolerated: exit 1 = no process running

	if _, err := d.r.Run(ctx, "ip", "addr", "flush", "dev", d.iface); err != nil {
		return fmt.Errorf("net: hostapd: StopAP: ip addr flush: %w", err)
	}
	return nil
}

// AttemptSTA stops the AP (freeing the radio for wpa_supplicant) then runs
// the shared wpa_cli/dhclient STA flow, writing an STA-only conf. The hostapd
// driver has no AP block of its own to retain in that conf (hostapd owns the
// AP separately, via renderHostapdConf).
func (d *hostapdDriver) AttemptSTA(ctx context.Context, sta STAConfig) error {
	if err := d.StopAP(ctx); err != nil {
		return fmt.Errorf("net: hostapd: AttemptSTA: %w", err)
	}
	render := func(s STAConfig) ([]byte, error) {
		body, err := renderSTAConf(s, d.country)
		return []byte(body), err
	}
	if err := staAttempt(ctx, d.r, d.iface, sta, render, d.writeFile, d.sleep); err != nil {
		return fmt.Errorf("net: hostapd: AttemptSTA: %w", err)
	}
	return nil
}

// APActive reports whether hostapd is currently running (pgrep exit 0 means
// a matching process was found; any other exit means none is running, not
// an error).
func (d *hostapdDriver) APActive(ctx context.Context) (bool, error) {
	_, err := d.r.Run(ctx, "pgrep", "-x", "hostapd")
	return err == nil, nil
}
