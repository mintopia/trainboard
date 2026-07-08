package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
)

// actionsTestPassword is the admin password newActionsTestServer sets up.
const actionsTestPassword = "longenough1"

// newActionsTestServer wires a Server over a valid, saved config with
// channels that receive a value each time the fake Actions.Apply/Reboot
// fire, and a way to make Reboot fail on demand (rebootErr).
func newActionsTestServer(t *testing.T) (srv *Server, applyCh, rebootCh chan struct{}, setRebootErr func(error)) {
	t.Helper()
	applyCh = make(chan struct{}, 1)
	rebootCh = make(chan struct{}, 1)

	var rebootErr error
	setRebootErr = func(err error) { rebootErr = err }

	svc, _ := newTestService(t, validCfg())
	svc.act = Actions{
		Apply: func() { applyCh <- struct{}{} },
		Reboot: func() error {
			rebootCh <- struct{}{}
			return rebootErr
		},
	}
	if err := svc.SetInitialPassword(actionsTestPassword, "PAD", ""); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	return NewServer(svc, testLog()), applyCh, rebootCh, setRebootErr
}

// (a) unauthenticated GET /actions redirects to /login.
func TestActionsGetUnauthenticatedRedirects(t *testing.T) {
	srv, _, _, _ := newActionsTestServer(t)
	rec := getPath(t, srv.Handler(), "/actions")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// (b) authed GET /actions shows all three forms: restart, reboot (with its
// confirm() guard), and the disabled update-firmware button.
func TestActionsGetShowsThreeForms(t *testing.T) {
	srv, _, _, _ := newActionsTestServer(t)
	cookie, _ := loginAs(t, srv, actionsTestPassword)

	rec := getPath(t, srv.Handler(), "/actions", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/actions/restart"`) {
		t.Fatalf("expected restart form action in body: %s", body)
	}
	if !strings.Contains(body, `action="/actions/reboot"`) {
		t.Fatalf("expected reboot form action in body: %s", body)
	}
	if !strings.Contains(body, `onsubmit="return confirm('Reboot the device?')"`) {
		t.Fatalf("expected reboot confirm() guard in body: %s", body)
	}
	if !strings.Contains(body, "disabled") || !strings.Contains(body, "coming in a later release") {
		t.Fatalf("expected disabled update-firmware button in body: %s", body)
	}
}

// (c) authed POST /actions/restart with the session's CSRF token fires
// Actions.Apply and renders the applied-style page.
func TestActionsRestartAuthedFiresApply(t *testing.T) {
	srv, applyCh, _, _ := newActionsTestServer(t)
	cookie, csrf := loginAs(t, srv, actionsTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/restart", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)
}

// (d) unauthenticated POST /actions/restart redirects to /login.
func TestActionsRestartUnauthenticatedRedirects(t *testing.T) {
	srv, _, _, _ := newActionsTestServer(t)
	rec := postForm(t, srv.Handler(), "/actions/restart", url.Values{})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// (e) authed POST /actions/restart missing/wrong csrf is rejected 403, and
// Actions.Apply is never fired.
func TestActionsRestartMissingCSRFRejected(t *testing.T) {
	srv, applyCh, _, _ := newActionsTestServer(t)
	cookie, _ := loginAs(t, srv, actionsTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/restart", url.Values{}, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertApplyNotCalled(t, applyCh)
}

// (f) authed POST /actions/reboot with the session's CSRF token fires
// Actions.Reboot and renders the "Rebooting…" page.
func TestActionsRebootAuthedFiresReboot(t *testing.T) {
	srv, _, rebootCh, _ := newActionsTestServer(t)
	cookie, csrf := loginAs(t, srv, actionsTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/reboot", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Rebooting") {
		t.Fatalf("expected 'Rebooting' in body: %s", rec.Body.String())
	}
	select {
	case <-rebootCh:
	default:
		t.Fatal("Actions.Reboot was not called")
	}
}

// (g) unauthenticated POST /actions/reboot redirects to /login.
func TestActionsRebootUnauthenticatedRedirects(t *testing.T) {
	srv, _, _, _ := newActionsTestServer(t)
	rec := postForm(t, srv.Handler(), "/actions/reboot", url.Values{})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// (h) authed POST /actions/reboot missing/wrong csrf is rejected 403, and
// Actions.Reboot is never fired.
func TestActionsRebootMissingCSRFRejected(t *testing.T) {
	srv, _, rebootCh, _ := newActionsTestServer(t)
	cookie, _ := loginAs(t, srv, actionsTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/reboot", url.Values{}, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-rebootCh:
		t.Fatal("Actions.Reboot must not be called")
	default:
	}
}

// (i) a Reboot error is surfaced as a 500 page, not the "Rebooting…" page.
func TestActionsRebootErrorRenders500(t *testing.T) {
	srv, _, rebootCh, setRebootErr := newActionsTestServer(t)
	setRebootErr(errors.New("boom: reboot command failed"))
	cookie, csrf := loginAs(t, srv, actionsTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/reboot", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "boom: reboot command failed") {
		t.Fatalf("expected error message in body: %s", rec.Body.String())
	}
	select {
	case <-rebootCh:
	default:
		t.Fatal("Actions.Reboot was not called")
	}
}

func TestActionsSoakStartCancelFlow(t *testing.T) {
	srv, svc, _, _ := newConfigTestServer(t)
	h := srv.Handler()
	cookie, csrf := loginAs(t, srv, configTestPassword)

	// Start with a valid duration: 302 back to /actions.
	form := url.Values{"duration": {"4h"}, "csrf": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/actions/soak", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/actions" {
		t.Fatalf("start soak: got %d -> %q, want 302 -> /actions", rec.Code, rec.Header().Get("Location"))
	}
	if got := svc.SoakRemaining(); got != 4*time.Hour {
		t.Fatalf("SoakRemaining = %v, want 4h", got)
	}

	// Actions page now shows the running soak + cancel form.
	req = httptest.NewRequest(http.MethodGet, "/actions", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "/actions/soak/cancel") {
		t.Fatal("actions page while soaking: no cancel form rendered")
	}

	// Cancel: 302 back, soak gone.
	form = url.Values{"csrf": {csrf}}
	req = httptest.NewRequest(http.MethodPost, "/actions/soak/cancel", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/actions" {
		t.Fatalf("cancel soak: got %d -> %q, want 302 -> /actions", rec.Code, rec.Header().Get("Location"))
	}
	if got := svc.SoakRemaining(); got != 0 {
		t.Fatalf("after cancel: SoakRemaining = %v, want 0", got)
	}
}

func TestActionsSoakInvalidDurationRerendersWithError(t *testing.T) {
	srv, svc, _, _ := newConfigTestServer(t)
	h := srv.Handler()
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := url.Values{"duration": {"12h"}, "csrf": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/actions/soak", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invalid duration: got %d, want 200 re-rendered form", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid soak duration") {
		t.Fatal("invalid duration: error message not rendered")
	}
	if got := svc.SoakRemaining(); got != 0 {
		t.Fatalf("invalid duration must not start a soak; SoakRemaining = %v", got)
	}
}

// (j) authed POST /actions/wifi-retry redirects to /actions and fires
// Service.WifiRetryNow (observed via the connectivity fake's retry count).
func TestActionsWifiRetryAuthedRedirectsAndRetries(t *testing.T) {
	srv, _, _, _, conn := newConnTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/wifi-retry", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/actions" {
		t.Fatalf("want 302 /actions, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if retries, _ := conn.counts(); retries != 1 {
		t.Fatalf("retries = %d, want 1", retries)
	}
}

// (k) unauthenticated POST /actions/wifi-retry redirects to /login.
func TestActionsWifiRetryUnauthenticatedRedirects(t *testing.T) {
	srv, _, _, _, _ := newConnTestServer(t)
	rec := postForm(t, srv.Handler(), "/actions/wifi-retry", url.Values{})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// (l) the JSON API mirror returns 200 {"status":"retrying"} and fires the
// same retry.
func TestAPIActionsWifiRetryReturnsJSONAndRetries(t *testing.T) {
	srv, _, _, _, conn := newConnTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	req := httptest.NewRequest(http.MethodPost, "/api/actions/wifi-retry", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"retrying"`) {
		t.Fatalf("expected retrying status in body: %s", rec.Body.String())
	}
	if retries, _ := conn.counts(); retries != 1 {
		t.Fatalf("retries = %d, want 1", retries)
	}
}

// (m) the actions page shows the retry form only while the hotspot fake
// reports AP mode active.
func TestActionsPageShowsRetryFormOnlyWhenHotspotActive(t *testing.T) {
	srv, _, _, _, conn := newConnTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/actions", cookie)
	if strings.Contains(rec.Body.String(), "/actions/wifi-retry") {
		t.Fatalf("retry form must not render without an active hotspot: %s", rec.Body.String())
	}

	conn.set(&board.Hotspot{SSID: "Trainboard-AB12", Password: "pw", Addr: "192.168.4.1"}, "")

	rec = getPath(t, srv.Handler(), "/actions", cookie)
	if !strings.Contains(rec.Body.String(), "/actions/wifi-retry") {
		t.Fatalf("expected retry form while hotspot active: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hotspot will drop") {
		t.Fatalf("expected hotspot-drop copy in body: %s", rec.Body.String())
	}
}

func TestStatusPageShowsSoakCountdown(t *testing.T) {
	srv, svc, _, _ := newConfigTestServer(t)
	h := srv.Handler()
	cookie, _ := loginAs(t, srv, configTestPassword)
	if err := svc.StartSoak("1h"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Soak") {
		t.Fatal("status page: no soak row while soaking")
	}
	if !strings.Contains(rec.Body.String(), "1h0m") {
		t.Fatalf("status page: remaining time not rendered; body: %.400s", rec.Body.String())
	}
}
