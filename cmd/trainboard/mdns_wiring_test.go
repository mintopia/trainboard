package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	netconn "github.com/mintopia/trainboard/internal/net"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/runtime"
	"github.com/mintopia/trainboard/internal/web"
)

// suffixShape matches macTail's output alone (four uppercase hex digits,
// e.g. "3456"), NOT the "Trainboard-XXXX" AP SSID that wraps it elsewhere.
var suffixShape = regexp.MustCompile(`^[0-9A-F]{4}$`)

// testWatchdog builds a Watchdog with a no-op systemd notifier, for tests
// that only need Register/Beat, not Run.
func testWatchdog() *obs.Watchdog {
	return obs.NewWatchdog(func(string) error { return nil }, time.Now)
}

// TestBuildMDNSConfigNilManagerSuppressWlan0Nil proves a nil manager
// (--manage-network off) yields a nil SuppressWlan0 — the mdns package's own
// documented "never suppress" default — rather than a func that always
// reports false, which would look identical from inside the responder but
// is a different decision to have made. It also pins the Suffix shape and
// that Beat is present and callable.
func TestBuildMDNSConfigNilManagerSuppressWlan0Nil(t *testing.T) {
	cfg := buildMDNSConfig(nil, testWatchdog(), testLog())

	if cfg.SuppressWlan0 != nil {
		t.Fatal("SuppressWlan0 must be nil when mgr is nil")
	}
	if !suffixShape.MatchString(cfg.Suffix) {
		t.Fatalf("Suffix = %q, want 4 uppercase hex chars", cfg.Suffix)
	}
	if cfg.Beat == nil {
		t.Fatal("Beat must not be nil")
	}
	cfg.Beat() // must not panic
}

// TestBuildMDNSConfigNonNilManagerSuppressesOnHotspot proves a non-nil
// manager wires a non-nil SuppressWlan0 that reads Status().Hotspot — false
// for a freshly constructed Manager, which publishes ManagerBoot with no
// hotspot yet.
func TestBuildMDNSConfigNonNilManagerSuppressesOnHotspot(t *testing.T) {
	mgr := netconn.NewManager(netconn.ManagerDeps{})
	cfg := buildMDNSConfig(mgr, testWatchdog(), testLog())

	if cfg.SuppressWlan0 == nil {
		t.Fatal("SuppressWlan0 must be non-nil when mgr is non-nil")
	}
	if cfg.SuppressWlan0() {
		t.Fatal("want false: a freshly constructed manager publishes no hotspot yet")
	}
}

// TestMDNSStateFuncDisabledIsNil proves --mdns=false yields a nil
// Sources.MDNSState closure — the web Service reads a nil source as ""
// (feature off).
func TestMDNSStateFuncDisabledIsNil(t *testing.T) {
	if got := mdnsStateFunc(false); got != nil {
		t.Fatal("mdnsStateFunc(false) returned a non-nil closure, want nil")
	}
}

// TestMDNSStateFuncEnabledReturnsHostname proves --mdns=true yields a
// closure over the static hostname derived from wlan0's MAC tail at boot
// (dev/test environments have no wlan0 device, so readWlanMAC falls back to
// "" and macTail("") to "0000" — matching startConnectivityManager's own
// AP-SSID fallback).
func TestMDNSStateFuncEnabledReturnsHostname(t *testing.T) {
	got := mdnsStateFunc(true)
	if got == nil {
		t.Fatal("mdnsStateFunc(true) = nil, want a closure")
	}
	if want := "trainboard-0000.local"; got() != want {
		t.Fatalf("mdnsStateFunc(true)() = %q, want %q", got(), want)
	}
}

// TestNewWebServiceWiresMDNSState proves newWebService threads mdnsState
// through to web.Service.MDNSState(), and that a nil mdnsState (mirroring
// --mdns=false) reads back as "".
func TestNewWebServiceWiresMDNSState(t *testing.T) {
	build := func(mdnsState func() string) *web.Service {
		return newWebService(
			filepath.Join(t.TempDir(), "config.json"),
			func() *board.Snapshot { return nil },
			obs.NewRing(4),
			func() []byte { return nil },
			time.Now(),
			&runtime.Soak{},
			webConnSeams{},
			mdnsState,
			nil, // upd: not under test here (see update_test.go)
			slog.New(slog.NewTextHandler(io.Discard, nil)),
		)
	}

	svc := build(func() string { return "trainboard-ab12.local" })
	if got := svc.MDNSState(); got != "trainboard-ab12.local" {
		t.Fatalf("svc.MDNSState() = %q, want trainboard-ab12.local", got)
	}

	svcOff := build(nil)
	if got := svcOff.MDNSState(); got != "" {
		t.Fatalf("svcOff.MDNSState() = %q, want empty", got)
	}
}
