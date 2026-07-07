package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

// apSetupFakes tracks the connectivity seam state newAPSetupTestServer wires
// up: the hotspot identity/last-error GET renders from, and a count of how
// many times Actions.WifiRetry fired.
type apSetupFakes struct {
	mu      sync.Mutex
	hs      *board.Hotspot
	lastErr string
	retries int
}

func (f *apSetupFakes) setLastErr(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastErr = msg
}

func (f *apSetupFakes) retryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retries
}

// newAPSetupTestServer wires a Server to a virgin device (no config file, no
// admin password) whose Sources.Hotspot reports AP mode active — the state a
// freshly-flashed board boots into per the M3 provisioning flow, before any
// setup has run. Unlike newVirginAPTestServer (handlers_portal_test.go),
// this harness also wires WifiRetry (counted) and a mutable LastSTAError, so
// tests can exercise SetupConnectivity's success path end to end.
func newAPSetupTestServer(t *testing.T) (srv *Server, svc *Service, path string, fakes *apSetupFakes) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "config.json")
	fakes = &apSetupFakes{hs: &board.Hotspot{SSID: "Trainboard-AB12", Password: "hotspotpw", Addr: "192.168.4.1"}}
	src := Sources{
		Snapshot:   func() *board.Snapshot { return nil },
		Ring:       obs.NewRing(8),
		PreviewPNG: func() []byte { return nil },
		Version:    "vtest",
		StartedAt:  time.Now(),
		Hotspot: func() *board.Hotspot {
			fakes.mu.Lock()
			defer fakes.mu.Unlock()
			return fakes.hs
		},
		LastSTAError: func() string {
			fakes.mu.Lock()
			defer fakes.mu.Unlock()
			return fakes.lastErr
		},
	}
	act := Actions{
		Apply:  func() {},
		Reboot: func() error { return nil },
		WifiRetry: func() {
			fakes.mu.Lock()
			defer fakes.mu.Unlock()
			fakes.retries++
		},
	}
	svc = NewService(path, src, act, testLog())
	srv = NewServer(svc, testLog())
	return srv, svc, path, fakes
}

// awaitWifiRetryCount waits up to 2s for fakes' retry count to reach at
// least want, polling rather than sleeping a fixed applyDelay so the test
// isn't racy against the scheduled time.AfterFunc.
func awaitWifiRetryCount(t *testing.T, fakes *apSetupFakes, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fakes.retryCount() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("WifiRetryNow was not called %d time(s) within timeout (got %d)", want, fakes.retryCount())
}

// (a) AP-mode GET /setup renders the WiFi SSID/password fields alongside the
// admin password fields, and surfaces a previously-recorded STA join error
// to the reconnecting provisioning user.
func TestSetupGetAPModeRendersWifiFieldsAndLastError(t *testing.T) {
	srv, _, _, fakes := newAPSetupTestServer(t)
	fakes.setLastErr("wifi: association failed")

	rec := getPath(t, srv.Handler(), "/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="ssid"`) {
		t.Fatalf("expected a wifi ssid field in AP-mode setup: %s", body)
	}
	if !strings.Contains(body, `name="psk"`) {
		t.Fatalf("expected a wifi psk field in AP-mode setup: %s", body)
	}
	if !strings.Contains(body, "wifi: association failed") {
		t.Fatalf("expected the last STA error surfaced: %s", body)
	}
}

// (b) LAN-mode GET /setup (no hotspot fake wired) renders the original
// three-field form with no wifi fields at all — the AP-mode branch must not
// leak into the non-AP path.
func TestSetupGetLANModeHasNoWifiFields(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := getPath(t, srv.Handler(), "/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `name="ssid"`) || strings.Contains(body, `name="psk"`) {
		t.Fatalf("LAN-mode setup must not render wifi fields: %s", body)
	}
}

// (c) AP-mode POST /setup with matching passwords and valid wifi credentials
// stores the password + wifi config, renders the wifi-done handoff page
// (not the LAN "restarting" page), does NOT schedule Actions.Apply (no
// restart in this path — Task 4's disk re-read picks up the new creds), and
// calls Service.WifiRetryNow once the response has been written.
func TestSetupPostAPModeSuccessRendersHandoffAndRetries(t *testing.T) {
	srv, _, path, fakes := newAPSetupTestServer(t)
	h := srv.Handler()

	form := url.Values{
		"password": {"longenough1"},
		"confirm":  {"longenough1"},
		"ssid":     {"MyHomeWifi"},
		"psk":      {"wifipassword1"},
	}
	rec := postForm(t, h, "/setup", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 handoff page, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "MyHomeWifi") {
		t.Fatalf("expected the joined SSID named in the handoff copy: %s", body)
	}
	if strings.Contains(body, "restarting") {
		t.Fatalf("AP-mode handoff must not use the LAN restart copy: %s", body)
	}
	// The stored document only meets the lighter ValidateConnectivity tier
	// (no origin/token collected in this flow yet), so it must be read back
	// with config.LoadRaw rather than config.Load/svc.VerifyLogin — both of
	// which apply the full board Validate() and would reject it here. That
	// full-vs-connectivity load distinction for post-partial-setup login is
	// a separate concern from this handoff flow (see the M3b plan's later
	// tasks for finishing setup at /config).
	stored, err := config.LoadRaw(path)
	if err != nil {
		t.Fatalf("stored config must at least parse: %v", err)
	}
	if err := stored.ValidateConnectivity(); err != nil {
		t.Fatalf("stored config must pass ValidateConnectivity: %v", err)
	}
	if !VerifyPassword(stored.Web.PasswordHash, "longenough1") {
		t.Fatal("admin password was not stored by AP-mode setup")
	}
	if stored.Wifi.SSID != "MyHomeWifi" || stored.Wifi.PSK != "wifipassword1" {
		t.Fatalf("wifi credentials not persisted: %+v", stored.Wifi)
	}

	awaitWifiRetryCount(t, fakes, 1)
}

// (d) AP-mode POST with mismatched confirm re-renders the AP-mode form with
// an error and stores nothing.
func TestSetupPostAPModeMismatchedConfirm(t *testing.T) {
	srv, svc, _, fakes := newAPSetupTestServer(t)
	form := url.Values{
		"password": {"longenough1"},
		"confirm":  {"different1"},
		"ssid":     {"MyHomeWifi"},
		"psk":      {"wifipassword1"},
	}
	rec := postForm(t, srv.Handler(), "/setup", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "class=\"error\"") {
		t.Fatalf("expected error markup in body: %s", rec.Body.String())
	}
	if svc.VerifyLogin("longenough1") {
		t.Fatal("password must not be set when confirm mismatches")
	}
	if fakes.retryCount() != 0 {
		t.Fatal("WifiRetryNow must not fire on a rejected submission")
	}
}

// (e) AP-mode POST with a blank SSID re-renders with SetupConnectivity's
// form-friendly error and stores nothing.
func TestSetupPostAPModeBlankSSIDRejected(t *testing.T) {
	srv, svc, _, _ := newAPSetupTestServer(t)
	form := url.Values{
		"password": {"longenough1"},
		"confirm":  {"longenough1"},
		"ssid":     {""},
		"psk":      {"wifipassword1"},
	}
	rec := postForm(t, srv.Handler(), "/setup", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wifi network name is required") {
		t.Fatalf("expected blank-ssid error in body: %s", rec.Body.String())
	}
	if svc.VerifyLogin("longenough1") {
		t.Fatal("password must not be set when wifi ssid is blank")
	}
}
