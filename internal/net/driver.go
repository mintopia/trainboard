package net

import "context"

// APConfig is the AP identity handed to a driver.
type APConfig struct {
	SSID     string // Trainboard-XXXX
	Password string // WPA2-PSK, 8-63 chars
	Addr     string // "192.168.4.1/24"
}

// STAConfig is the target client network.
type STAConfig struct{ SSID, PSK string }

// apDriver abstracts "make the AP exist / attempt the STA network".
// Implementations: mode2 (single wpa_supplicant), hostapd (Task 6).
type apDriver interface {
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
