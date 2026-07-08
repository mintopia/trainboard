package net

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// APConfig is the AP identity handed to a driver.
type APConfig struct {
	SSID     string // Trainboard-XXXX
	Password string // WPA2-PSK, 8-63 chars
	Addr     string // "192.168.4.1/24"
}

// STAConfig is the target client network.
type STAConfig struct{ SSID, PSK string }

// Driver abstracts "make the AP exist / attempt the STA network".
// Implementations: mode2 (single wpa_supplicant), hostapd (fallback).
// Exported (Task 12) so cmd/trainboard can name ManagerDeps.Driver's type
// when wiring the production driver.
type Driver interface {
	// StartAP brings the AP up (and assigns APConfig.Addr to the iface).
	StartAP(ctx context.Context, ap APConfig) error
	// StopAP tears the AP down (does NOT start STA).
	StopAP(ctx context.Context) error
	// AttemptSTA switches to the client network and runs dhclient; it does
	// NOT evaluate connectivity beyond association+DHCP client exit — the
	// layered Check owns that.
	AttemptSTA(ctx context.Context, sta STAConfig) error
	// APActive reports whether the AP is currently beaconing (used by the
	// AP-restore invariant).
	APActive(ctx context.Context) (bool, error)
}

// pollAttempts and pollInterval bound the wait for wpa_supplicant to reach
// the wanted state after a select_network. Shared by both Driver
// implementations.
const pollAttempts = 10

const pollInterval = 500 * time.Millisecond

// dhclientPidfile is where staAttempt's daemon-mode dhclient (issue #46)
// writes its pid (-pf). It is the sole handle killDHClient uses to find and
// stop that daemon — dhclient itself owns renewal from here on, so nothing
// else in this package touches the lease after staAttempt starts it.
const dhclientPidfile = "/run/trainboard-dhclient.pid"

// killDHClient stops any dhclient daemon previously started against
// dhclientPidfile (pkill -F reads the pid from the file). It tolerates any
// failure — most commonly pkill's exit 1, "no matching process", when no
// daemon was ever started (first boot) or a prior attempt never got far
// enough to spawn one — because a failed kill must never abort the caller's
// STA attempt or AP transition; the daemon either wasn't running (fine) or
// killing it failed for some other reason we have no safe recovery from
// here anyway. Called at the start of every staAttempt (kill-before-start,
// so a stale renewer from a previous attempt never races the new one) and
// from every driver path that leaves STA for AP (mode2Driver.StartAP,
// hostapdDriver.StartAP), so the daemon never outlives the STA session that
// started it.
func killDHClient(ctx context.Context, r Runner) {
	_, _ = r.Run(ctx, "pkill", "-F", dhclientPidfile)
}

// pollStatus polls `wpa_cli status` up to attempts times, pollInterval
// apart, until want reports true. It returns an error naming failMsg if the
// state is never reached. Callers pass pollAttempts for the default budget;
// StartAP passes apPollsAfterDaemonStart after a cold daemon spawn (issue
// #48).
func pollStatus(ctx context.Context, r Runner, iface string, sleep func(time.Duration), attempts int, want func(map[string]string) bool, failMsg string) error {
	for i := 0; i < attempts; i++ {
		out, err := r.Run(ctx, "wpa_cli", "-i", iface, "status")
		if err == nil && want(parseWpaStatus(out)) {
			return nil
		}
		if i < attempts-1 {
			sleep(pollInterval)
		}
	}
	return fmt.Errorf("net: %s after %d polls", failMsg, attempts)
}

// renderSTAConf formats a wpa_supplicant conf containing only the STA
// network block; used by staAttempt. The wpa conf format has no escaping for
// quoted strings, so any value containing a `"` is rejected outright.
// country is the configured regulatory domain (defaulted to "GB" by the
// caller when unset).
func renderSTAConf(sta STAConfig, country string) (string, error) {
	for _, v := range []string{sta.SSID, sta.PSK} {
		if strings.Contains(v, `"`) {
			return "", fmt.Errorf("net: staAttempt: value contains disallowed quote character")
		}
	}
	return fmt.Sprintf(`ctrl_interface=/run/wpa_supplicant
country=%s
network={
    id_str="sta"
    ssid="%s"
    psk="%s"
    disabled=1
}
`, country, sta.SSID, sta.PSK), nil
}

// staAttempt runs the "switch to the STA network and obtain a DHCP lease"
// flow shared by both apDriver implementations: kill any dhclient daemon
// left over from a previous attempt (issue #46 kill-before-start — a stale
// renewer must never race the new attempt's lease), render the conf via
// renderConf (letting each driver decide what else belongs in the file —
// mode2Driver retains its AP block, hostapdDriver renders STA-only since
// hostapd owns the AP separately), persist it, tell wpa_supplicant to reload
// it, select network 0, wait for association, then start dhclient in daemon
// mode (-pf dhclientPidfile, no -1) so it keeps renewing the lease for as
// long as the STA session lasts — see killDHClient's doc comment for who
// stops it and when. It does not evaluate connectivity beyond dhclient's
// initial exit (it daemonizes once it has a lease) — the layered Check owns
// that.
func staAttempt(ctx context.Context, r Runner, iface string, sta STAConfig, renderConf func(STAConfig) ([]byte, error), writeFile func(string, []byte) error, sleep func(time.Duration)) error {
	killDHClient(ctx, r)

	body, err := renderConf(sta)
	if err != nil {
		return fmt.Errorf("net: staAttempt: %w", err)
	}
	if err := writeFile(wpaConfPath, body); err != nil {
		return fmt.Errorf("net: staAttempt: write conf: %w", err)
	}
	if _, err := r.Run(ctx, "wpa_cli", "-i", iface, "reconfigure"); err != nil {
		return fmt.Errorf("net: staAttempt: reconfigure: %w", err)
	}
	if _, err := r.Run(ctx, "wpa_cli", "-i", iface, "select_network", "0"); err != nil {
		return fmt.Errorf("net: staAttempt: select_network 0: %w", err)
	}
	if err := pollStatus(ctx, r, iface, sleep, pollAttempts, func(kv map[string]string) bool {
		return kv["wpa_state"] == "COMPLETED"
	}, "STA not associated"); err != nil {
		return fmt.Errorf("net: staAttempt: %w", err)
	}
	if _, err := r.Run(ctx, "dhclient", "-v", "-pf", dhclientPidfile, iface); err != nil {
		return fmt.Errorf("net: staAttempt: dhclient: %w", err)
	}
	return nil
}
