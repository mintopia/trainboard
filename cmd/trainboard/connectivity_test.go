package main

import (
	"errors"
	"os"
	"testing"

	"github.com/mintopia/trainboard/internal/config"
	netconn "github.com/mintopia/trainboard/internal/net"
)

func TestMacTailNormalMAC(t *testing.T) {
	got := macTail("b8:27:eb:12:34:56")
	if got != "3456" {
		t.Fatalf("macTail = %q, want %q", got, "3456")
	}
}

func TestMacTailUppercases(t *testing.T) {
	got := macTail("de:ad:be:ef:ca:fe")
	if got != "CAFE" {
		t.Fatalf("macTail = %q, want %q", got, "CAFE")
	}
}

// TestMacTailMalformedFallsBack locks in macTail's documented fallback
// behaviour (readWlanMAC returning "" on any read error is the main real
// caller of this path) for anything shaped unexpectedly: empty string,
// no colons at all, and a single colon-separated field.
func TestMacTailMalformedFallsBack(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no colons", "deadbeefcafe"},
		{"single field", "onlyone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := macTail(tc.in); got != "0000" {
				t.Fatalf("macTail(%q) = %q, want %q", tc.in, got, "0000")
			}
		})
	}
}

// TestMacTailShortStillCombinesLastTwoParts documents current behaviour for
// a MAC-shaped-but-short input: with exactly two colon-separated parts,
// macTail still combines and uppercases them rather than falling back,
// since the >= 2 check only guards against fewer than two parts.
func TestMacTailShortStillCombinesLastTwoParts(t *testing.T) {
	got := macTail("ab:cd")
	if got != "ABCD" {
		t.Fatalf("macTail(%q) = %q, want %q", "ab:cd", got, "ABCD")
	}
}

// TestResolveE04ConfigPrefersRawWhenPresent proves the E04-path helper
// carries a raw-loaded config's Provisioning (and Web) fields forward
// instead of falling back to config.Default() when config.LoadRaw
// succeeded — the crux of the AP-password-churn fix: a previously
// configured device that merely fails board Validate() must not lose its
// persisted AP password on every E04 boot.
func TestResolveE04ConfigPrefersRawWhenPresent(t *testing.T) {
	raw := config.Default()
	raw.Provisioning.APPassword = "persisted-pw"
	raw.Web.PasswordHash = "$argon2id$fake"

	got := resolveE04Config(raw, nil)

	if got.Provisioning.APPassword != "persisted-pw" {
		t.Fatalf("resolveE04Config dropped persisted APPassword: %+v", got.Provisioning)
	}
	if got.Web.PasswordHash != "$argon2id$fake" {
		t.Fatalf("resolveE04Config dropped Web.PasswordHash: %+v", got.Web)
	}
}

// TestResolveE04ConfigFallsBackOnRawError covers the genuinely
// wholly-unreadable/unparsable-file case: resolveE04Config must not surface
// a half-populated zero Config in that case, falling back to
// config.Default() instead.
func TestResolveE04ConfigFallsBackOnRawError(t *testing.T) {
	raw := config.Config{} // zero value: what LoadRaw returns alongside a non-nil error
	got := resolveE04Config(raw, errors.New("config: parsing boom: bad json"))

	want := config.Default()
	if got.Provisioning.APPassword != want.Provisioning.APPassword || got.Board.Services != want.Board.Services {
		t.Fatalf("resolveE04Config on rawErr != nil = %+v, want config.Default() %+v", got, want)
	}
}

// TestSTAFromDiskReadsOnEveryCall proves that staFromDisk returns a closure
// that re-reads the config from disk on every call — the core of the
// credential-handoff flow: portal saves new WiFi creds, staFromDisk closure
// returns them on the next call without a process restart.
func TestSTAFromDiskReadsOnEveryCall(t *testing.T) {
	tmpdir := t.TempDir()
	cfgPath := tmpdir + "/config.json"

	// Helper: write a config with given SSID/PSK, with Web.PasswordHash set
	// so config.SaveConnectivity can validate it (connectivity tier).
	writeTestConfig := func(ssid, psk string) {
		cfg := config.Default()
		cfg.Web.PasswordHash = "$argon2id$v=19$m=19456,t=2,p=1$testhashabcdefgh$testhashabcdefghij"
		cfg.Wifi.SSID = ssid
		cfg.Wifi.PSK = psk
		if err := config.SaveConnectivity(cfgPath, cfg); err != nil {
			t.Fatalf("SaveConnectivity failed: %v", err)
		}
	}

	sta := staFromDisk(cfgPath)

	// First write: initial creds
	writeTestConfig("InitialSSID", "initialPSK")
	first := sta()
	if first.SSID != "InitialSSID" || first.PSK != "initialPSK" {
		t.Fatalf("First call: got %+v, want SSID=InitialSSID PSK=initialPSK", first)
	}

	// Overwrite file: new creds
	writeTestConfig("UpdatedSSID", "updatedPSK")
	second := sta()
	if second.SSID != "UpdatedSSID" || second.PSK != "updatedPSK" {
		t.Fatalf("Second call after file update: got %+v, want SSID=UpdatedSSID PSK=updatedPSK", second)
	}

	// Missing file: zero STAConfig
	if err := os.Remove(cfgPath); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	third := sta()
	zero := netconn.STAConfig{}
	if third != zero {
		t.Fatalf("Missing file call: got %+v, want zero STAConfig %+v", third, zero)
	}
}
