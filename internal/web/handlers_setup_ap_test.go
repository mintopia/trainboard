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
	fakes = &apSetupFakes{hs: &board.Hotspot{SSID: "Trainboard-AB12", Addr: "192.168.4.1"}}
	src := Sources{
		Snapshot:  func() *board.Snapshot { return nil },
		Ring:      obs.NewRing(8),
		Version:   "vtest",
		StartedAt: time.Now(),
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

// newProvisionedAPFallbackTestServer wires a Server to a PROVISIONED device —
// admin password + WiFi creds already on disk (the connectivity-valid,
// board-invalid partial-setup document) — whose Hotspot() nonetheless reports
// AP mode active. This is AP FALLBACK: the state a board lands in when it
// cannot join its configured WiFi (a failed join, a router swap, an outage),
// as opposed to the virgin never-provisioned device every other AP-setup test
// uses. lastErr seeds Service.LastSTAError. Pass hotspot=false to model the
// same provisioned device back in LAN mode (Hotspot()==nil).
func newProvisionedAPFallbackTestServer(t *testing.T, lastErr string, hotspot bool) (*Server, *Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	hash, err := HashPassword("longenough1")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	partial := config.Default() // empty origin/token: board-invalid, connectivity-valid
	partial.Web.PasswordHash = hash
	partial.Wifi.SSID = "HomeNet"
	partial.Wifi.PSK = "supersecret"
	if err := config.SaveConnectivity(path, partial); err != nil {
		t.Fatalf("seed provisioned config: %v", err)
	}
	src := Sources{
		Snapshot:     func() *board.Snapshot { return nil },
		Ring:         obs.NewRing(8),
		Version:      "vtest",
		StartedAt:    time.Now(),
		LastSTAError: func() string { return lastErr },
	}
	if hotspot {
		src.Hotspot = func() *board.Hotspot {
			return &board.Hotspot{SSID: "Trainboard-AB12", Addr: "192.168.4.1"}
		}
	}
	act := Actions{Apply: func() {}, Reboot: func() error { return nil }}
	svc := NewService(path, src, act, testLog())
	return NewServer(svc, testLog()), svc
}

// (f) A provisioned device in AP fallback serves a READ-ONLY status view on
// GET /setup: it surfaces the last STA join error (unreachable otherwise,
// since the AP-mode setup form is gated off once a password exists), names the
// configured SSID, points the user at /login for "Retry WiFi now", and — being
// a pre-auth page — renders NONE of the WiFi/password form fields.
func TestSetupGetProvisionedAPFallbackServesReadOnlyStatus(t *testing.T) {
	srv, _ := newProvisionedAPFallbackTestServer(t, "wifi: association failed", true)
	rec := getPath(t, srv.Handler(), "/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 read-only status view, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "wifi: association failed") {
		t.Fatalf("expected the last STA error surfaced: %s", body)
	}
	if !strings.Contains(body, "HomeNet") {
		t.Fatalf("expected the configured SSID named: %s", body)
	}
	if !strings.Contains(body, "http://192.168.4.1/login") {
		t.Fatalf("expected /login guidance for Retry WiFi now: %s", body)
	}
	// The wrong-details correction path is Configuration -> Network (where
	// wifi.ssid/psk actually live once signed in) — NOT a link back to
	// /setup, which for a provisioned+hotspot device only ever re-serves
	// this same read-only view (GET) or 404s (POST): a dead end.
	if !strings.Contains(body, "Configuration") || !strings.Contains(body, "Network") {
		t.Fatalf("expected Configuration > Network correction guidance: %s", body)
	}
	if strings.Contains(body, `href="/setup"`) {
		t.Fatalf("read-only status view must not link back to /setup (dead end): %s", body)
	}
	if strings.Contains(body, `name="ssid"`) || strings.Contains(body, `name="psk"`) ||
		strings.Contains(body, `name="password"`) || strings.Contains(body, "<form") {
		t.Fatalf("read-only status view must not render any setup form fields: %s", body)
	}
}

// (g) With no recorded error yet, the read-only view still renders (200) with
// "still trying" style copy rather than a blank/error page.
func TestSetupGetProvisionedAPFallbackNoErrorStillRenders(t *testing.T) {
	srv, _ := newProvisionedAPFallbackTestServer(t, "", true)
	rec := getPath(t, srv.Handler(), "/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `name="ssid"`) || strings.Contains(body, "<form") {
		t.Fatalf("read-only status view must not render a form: %s", body)
	}
}

// (h) POST /setup on a provisioned AP-fallback device stays refused (404 via
// the gate) exactly as before — the read-only view is GET-only.
func TestSetupPostProvisionedAPFallbackRefused(t *testing.T) {
	srv, _ := newProvisionedAPFallbackTestServer(t, "", true)
	form := url.Values{
		"password": {"longenough1"}, "confirm": {"longenough1"},
		"ssid": {"Evil"}, "psk": {"wifipassword1"},
	}
	rec := postForm(t, srv.Handler(), "/setup", form)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /setup on a provisioned device must 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// (i) The captive-portal probe redirect chain still lands somewhere useful:
// the probe 302s to the absolute setup URL, and that URL now serves the 200
// read-only status view instead of a dead 404.
func TestPortalProbeTargetReachableWhenProvisioned(t *testing.T) {
	srv, _ := newProvisionedAPFallbackTestServer(t, "boom", true)
	h := srv.Handler()
	rec := getPath(t, h, "/generate_204")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != apSetupURL {
		t.Fatalf("probe: want 302 %s, got %d %q", apSetupURL, rec.Code, rec.Header().Get("Location"))
	}
	rec = getPath(t, h, "/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("probe target /setup must be reachable (200), got %d body=%s", rec.Code, rec.Body.String())
	}
}

// (j) LAN mode (Hotspot()==nil) is byte-identical to before: a provisioned
// device 404s /setup for both GET and POST.
func TestSetupProvisionedLANModeStill404s(t *testing.T) {
	srv, _ := newProvisionedAPFallbackTestServer(t, "", false)
	h := srv.Handler()
	if rec := getPath(t, h, "/setup"); rec.Code != http.StatusNotFound {
		t.Fatalf("LAN-mode provisioned GET /setup must 404, got %d", rec.Code)
	}
	form := url.Values{"password": {"longenough1"}, "confirm": {"longenough1"}}
	if rec := postForm(t, h, "/setup", form); rec.Code != http.StatusNotFound {
		t.Fatalf("LAN-mode provisioned POST /setup must 404, got %d", rec.Code)
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
