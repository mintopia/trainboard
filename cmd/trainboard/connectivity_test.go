package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	netconn "github.com/mintopia/trainboard/internal/net"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/runtime"
	"github.com/mintopia/trainboard/internal/web"
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

// TestWifiCountryDefaultsToGBWhenUnset proves startConnectivityManager's
// country-resolution helper falls back to GB for a config whose Wifi.Country
// hasn't been set (old config document, or fresh Default()), matching the
// consumer-side default documented on config.WifiConfig.Country.
func TestWifiCountryDefaultsToGBWhenUnset(t *testing.T) {
	cfg := config.Default()
	cfg.Wifi.Country = ""
	if got := wifiCountry(cfg); got != "GB" {
		t.Fatalf("wifiCountry() = %q, want %q", got, "GB")
	}
}

// TestWifiCountryUsesConfiguredValue proves a configured country passes
// through unchanged rather than being silently forced to GB.
func TestWifiCountryUsesConfiguredValue(t *testing.T) {
	cfg := config.Default()
	cfg.Wifi.Country = "US"
	if got := wifiCountry(cfg); got != "US" {
		t.Fatalf("wifiCountry() = %q, want %q", got, "US")
	}
}

// TestResolveE04ConfigPrefersRawWhenPresent proves the E04-path helper
// carries a raw-loaded config's Web fields forward instead of falling back
// to config.Default() when config.LoadRaw succeeded — a previously
// configured device that merely fails board Validate() must not lose its
// persisted admin password hash on every E04 boot.
func TestResolveE04ConfigPrefersRawWhenPresent(t *testing.T) {
	raw := config.Default()
	raw.Web.PasswordHash = "$argon2id$fake"

	got := resolveE04Config(raw, nil)

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
	if got.Board.Services != want.Board.Services {
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

// fakeConnManager implements the connManager seam the web-wiring adapter is
// built against, recording every interaction so tests can assert the mapping
// (Status fields -> read seams, RetryNow/NoteProvisioning -> action seams).
type fakeConnManager struct {
	status  netconn.Status
	retries int
	notedAt []time.Time
}

func (f *fakeConnManager) Status() netconn.Status         { return f.status }
func (f *fakeConnManager) RetryNow()                      { f.retries++ }
func (f *fakeConnManager) NoteProvisioning(now time.Time) { f.notedAt = append(f.notedAt, now) }

// TestNewWebConnSeamsMapsManagerState proves the adapter maps the manager's
// published state onto the four web seams: Status().Hotspot -> hotspot,
// Status().LastSTAErr -> lastSTAError, RetryNow -> wifiRetry, and
// NoteProvisioning -> noteProvisioning stamped with the injected clock.
func TestNewWebConnSeamsMapsManagerState(t *testing.T) {
	hs := &board.Hotspot{SSID: "Trainboard-AB12", Addr: "192.168.4.1"}
	fake := &fakeConnManager{status: netconn.Status{
		State:      netconn.ManagerAPFallback,
		Hotspot:    hs,
		LastSTAErr: "sta: join failed: wrong PSK",
	}}
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	seams := newWebConnSeams(fake, func() time.Time { return now })

	if got := seams.hotspot(); got != hs {
		t.Fatalf("hotspot() = %v, want the manager's published %v", got, hs)
	}
	if got := seams.lastSTAError(); got != "sta: join failed: wrong PSK" {
		t.Fatalf("lastSTAError() = %q", got)
	}
	seams.wifiRetry()
	if fake.retries != 1 {
		t.Fatalf("wifiRetry: RetryNow called %d times, want 1", fake.retries)
	}
	seams.noteProvisioning()
	if len(fake.notedAt) != 1 || !fake.notedAt[0].Equal(now) {
		t.Fatalf("noteProvisioning: NoteProvisioning calls = %v, want exactly one at %v", fake.notedAt, now)
	}
}

// TestNewWebConnSeamsReadsLiveStatus proves the read seams re-read
// Status() on every call rather than capturing a boot-time snapshot — the
// manager republishes state as it moves between STA and AP.
func TestNewWebConnSeamsReadsLiveStatus(t *testing.T) {
	fake := &fakeConnManager{} // zero Status: no hotspot, no error
	seams := newWebConnSeams(fake, time.Now)

	if got := seams.hotspot(); got != nil {
		t.Fatalf("hotspot() before AP = %v, want nil", got)
	}
	if got := seams.lastSTAError(); got != "" {
		t.Fatalf("lastSTAError() before any failure = %q, want empty", got)
	}

	fake.status = netconn.Status{
		State:      netconn.ManagerAPFallback,
		Hotspot:    &board.Hotspot{SSID: "Trainboard-CAFE", Addr: "192.168.4.1"},
		LastSTAErr: "sta: timeout",
	}
	if got := seams.hotspot(); got == nil || got.SSID != "Trainboard-CAFE" {
		t.Fatalf("hotspot() after publish = %v, want the fresh status", got)
	}
	if got := seams.lastSTAError(); got != "sta: timeout" {
		t.Fatalf("lastSTAError() after publish = %q", got)
	}
}

// TestNewWebServiceWiresConnSeams drives the actual construction path both
// boot paths use (newWebService) and checks the seams surface through the
// web Service exactly as the manager publishes them.
func TestNewWebServiceWiresConnSeams(t *testing.T) {
	hs := &board.Hotspot{SSID: "Trainboard-AB12", Addr: "192.168.4.1"}
	fake := &fakeConnManager{status: netconn.Status{Hotspot: hs, LastSTAErr: "sta: boom"}}
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	svc := newTestWebService(t, newWebConnSeams(fake, func() time.Time { return now }))

	if got := svc.Hotspot(); got != hs {
		t.Fatalf("svc.Hotspot() = %v, want the manager's %v", got, hs)
	}
	if got := svc.LastSTAError(); got != "sta: boom" {
		t.Fatalf("svc.LastSTAError() = %q", got)
	}
	svc.WifiRetryNow()
	if fake.retries != 1 {
		t.Fatalf("svc.WifiRetryNow(): RetryNow called %d times, want 1", fake.retries)
	}
	svc.MarkProvisioning()
	if len(fake.notedAt) != 1 || !fake.notedAt[0].Equal(now) {
		t.Fatalf("svc.MarkProvisioning(): NoteProvisioning calls = %v, want one at %v", fake.notedAt, now)
	}
}

// TestNewWebServiceSeamsNilWithoutManageNetwork pins the --manage-network-off
// contract: both boot paths pass the zero webConnSeams, and the web Service
// must read as "no connectivity manager" (nil hotspot, empty error) with the
// action seams as safe no-ops — the nil-tolerance web.Sources/Actions
// document.
func TestNewWebServiceSeamsNilWithoutManageNetwork(t *testing.T) {
	svc := newTestWebService(t, webConnSeams{})

	if got := svc.Hotspot(); got != nil {
		t.Fatalf("Hotspot() with no manager = %v, want nil", got)
	}
	if got := svc.LastSTAError(); got != "" {
		t.Fatalf("LastSTAError() with no manager = %q, want empty", got)
	}
	svc.WifiRetryNow()     // must be a safe no-op
	svc.MarkProvisioning() // must be a safe no-op
}

// newTestWebService builds a web.Service through the production newWebService
// constructor with harness stand-ins for everything except the connectivity
// seams under test.
func newTestWebService(t *testing.T, conn webConnSeams) *web.Service {
	t.Helper()
	return newWebService(
		filepath.Join(t.TempDir(), "config.json"),
		func() *board.Snapshot { return nil },
		obs.NewRing(4),
		func() []byte { return nil },
		time.Now(),
		&runtime.Soak{},
		conn,
		nil, // mdnsState: not under test here (see mdns_wiring_test.go)
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}
