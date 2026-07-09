package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/update"
)

// updateTestPassword is the admin password newUpdateTestServer sets up.
const updateTestPassword = "longenough1"

// updateCalls counts how many times each update seam fired.
type updateCalls struct {
	checks, applies, dismisses int
}

// newUpdateTestServer wires a Server over a valid, saved config (mirroring
// newConfigTestServerCore's shape in handlers_config_test.go) whose update
// seams (Sources.UpdateStatus, Actions.UpdateCheck/UpdateApply/UpdateDismiss)
// report st and record calls into the returned updateCalls. applyErr makes
// Actions.UpdateApply fail on demand, mirroring newActionsTestServer's
// setRebootErr pattern.
func newUpdateTestServer(t *testing.T, st update.Status, applyErr error) (srv *Server, calls *updateCalls, applyCh chan struct{}) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	cfg.Board.Origin = "PAD"
	cfg.Darwin.Token = "tok-update-test"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	calls = &updateCalls{}
	applyCh = make(chan struct{}, 1)
	src := Sources{
		Snapshot:     func() *board.Snapshot { return nil },
		Ring:         obs.NewRing(8),
		Version:      "v0.1.0",
		StartedAt:    time.Now(),
		UpdateStatus: func() update.Status { return st },
	}
	act := Actions{
		Apply:         func() { applyCh <- struct{}{} },
		UpdateCheck:   func(_ context.Context) error { calls.checks++; return nil },
		UpdateApply:   func(_ context.Context) error { calls.applies++; return applyErr },
		UpdateDismiss: func() error { calls.dismisses++; return nil },
	}
	svc := NewService(path, src, act, testLog())
	if err := svc.SetInitialPassword(updateTestPassword, "PAD", ""); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	return NewServer(svc, testLog()), calls, applyCh
}

// (a) the status page renders the full Software section when the updater is
// enabled: running/available versions, notes link, rollback banner, last
// error, and the three action forms.
func TestStatusPageShowsSoftwareSection(t *testing.T) {
	st := update.Status{
		Enabled: true, Running: "v0.1.0", Available: "v0.2.0",
		NotesURL:       "https://github.com/mintopia/trainboard/releases/tag/v0.2.0",
		RolledBackFrom: "v0.1.9", LastError: "boom",
	}
	srv, _, _ := newUpdateTestServer(t, st, nil)
	cookie, _ := loginAs(t, srv, updateTestPassword)

	rec := getPath(t, srv.Handler(), "/", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"v0.2.0",
		"Rolled back from v0.1.9",
		"boom",
		`action="/actions/update/check"`,
		`action="/actions/update/apply"`,
		`action="/actions/update/dismiss"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in body: %s", want, body)
		}
	}
}

// (b) a zero update.Status (Enabled=false) hides every control, even though
// the Software section itself still renders (self-review: graceful
// degradation, not a missing section).
func TestStatusPageHidesControlsWhenDisabled(t *testing.T) {
	srv, _, _ := newUpdateTestServer(t, update.Status{}, nil)
	cookie, _ := loginAs(t, srv, updateTestPassword)

	rec := getPath(t, srv.Handler(), "/", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Software") {
		t.Fatalf("expected Software section heading even when disabled: %s", body)
	}
	for _, unwanted := range []string{"/actions/update/apply", "/actions/update/check", "/actions/update/dismiss"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("update controls must be hidden when disabled (found %q): %s", unwanted, body)
		}
	}
}

// (c) POST /actions/update/check with a valid CSRF token fires
// Service.CheckForUpdate and redirects (PRG) to the status page.
func TestUpdateCheckActionCallsSeamAndRedirects(t *testing.T) {
	srv, calls, _ := newUpdateTestServer(t, update.Status{Enabled: true}, nil)
	cookie, csrf := loginAs(t, srv, updateTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/update/check", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("want 302 /, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if calls.checks != 1 {
		t.Fatalf("calls.checks = %d, want 1", calls.checks)
	}
}

// (d) a successful POST /actions/update/apply renders the same applied/
// restart copy config save uses, and schedules the same clean-exit restart
// (Actions.Apply) via scheduleApply.
func TestUpdateApplySuccessRendersAppliedAndSchedulesRestart(t *testing.T) {
	srv, calls, applyCh := newUpdateTestServer(t, update.Status{Enabled: true, Available: "v0.2.0"}, nil)
	cookie, csrf := loginAs(t, srv, updateTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/update/apply", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "restarting") {
		t.Fatalf("expected applied/restart copy in body: %s", rec.Body.String())
	}
	if calls.applies != 1 {
		t.Fatalf("calls.applies = %d, want 1", calls.applies)
	}
	awaitApply(t, applyCh)
}

// (e) an apply failure redirects to the status page (whose Software section
// shows Status.LastError) instead of rendering the applied page, and never
// schedules a restart — the current binary keeps running.
func TestUpdateApplyFailureRedirectsWithoutRestart(t *testing.T) {
	srv, calls, applyCh := newUpdateTestServer(t, update.Status{Enabled: true}, errors.New("boom: apply failed"))
	cookie, csrf := loginAs(t, srv, updateTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/update/apply", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("want 302 /, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if calls.applies != 1 {
		t.Fatalf("calls.applies = %d, want 1", calls.applies)
	}
	assertApplyNotCalled(t, applyCh)
}

// (f) POST /actions/update/dismiss fires Service.DismissRollback and
// redirects (PRG) to the status page.
func TestUpdateDismissActionRedirects(t *testing.T) {
	srv, calls, _ := newUpdateTestServer(t, update.Status{Enabled: true, RolledBackFrom: "v0.1.9"}, nil)
	cookie, csrf := loginAs(t, srv, updateTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/update/dismiss", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("want 302 /, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if calls.dismisses != 1 {
		t.Fatalf("calls.dismisses = %d, want 1", calls.dismisses)
	}
}

// (g) GET /api/status includes the update status under the "update" key.
func TestAPIStatusIncludesUpdate(t *testing.T) {
	srv, _, _ := newUpdateTestServer(t, update.Status{Enabled: true, Running: "v0.1.0", Available: "v0.2.0"}, nil)
	cookie, _ := loginAs(t, srv, updateTestPassword)

	rec := getPath(t, srv.Handler(), "/api/status", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"update":{"enabled":true`) {
		t.Fatalf("expected update status in body: %s", rec.Body.String())
	}
}

// (h) the JSON API mirrors for check/apply return the API's standard success
// shape and fire the same seams as their HTML counterparts.
func TestAPIUpdateActions(t *testing.T) {
	srv, calls, applyCh := newUpdateTestServer(t, update.Status{Enabled: true}, nil)
	cookie, csrf := loginAs(t, srv, updateTestPassword)

	req := httptest.NewRequest(http.MethodPost, "/api/actions/update/check", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("check: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if calls.checks != 1 {
		t.Fatalf("calls.checks = %d, want 1", calls.checks)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/actions/update/apply", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"applied"`) {
		t.Fatalf("expected applied status in body: %s", rec.Body.String())
	}
	if calls.applies != 1 {
		t.Fatalf("calls.applies = %d, want 1", calls.applies)
	}
	awaitApply(t, applyCh)
}

// (i) an apply failure via the JSON API returns the API's standard
// {"error": ...} shape and never schedules a restart.
func TestAPIUpdateApplyFailureReturnsJSONError(t *testing.T) {
	srv, _, applyCh := newUpdateTestServer(t, update.Status{Enabled: true}, errors.New("boom: apply failed"))
	cookie, csrf := loginAs(t, srv, updateTestPassword)

	req := httptest.NewRequest(http.MethodPost, "/api/actions/update/apply", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("expected uniform error shape in body: %s", rec.Body.String())
	}
	assertApplyNotCalled(t, applyCh)
}

// (j) a server with zero Actions (no update seams wired at all — the
// dev-mode / not-wired case) must not panic on any of the new routes; the
// nil-safe Service methods surface a graceful error instead.
func TestUpdateActionsNilSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	cfg.Board.Origin = "PAD"
	cfg.Darwin.Token = "tok-update-test"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	applyCh := make(chan struct{}, 1)
	src := Sources{
		Snapshot:  func() *board.Snapshot { return nil },
		Ring:      obs.NewRing(8),
		Version:   "vtest",
		StartedAt: time.Now(),
	}
	act := Actions{Apply: func() { applyCh <- struct{}{} }}
	svc := NewService(path, src, act, testLog())
	if err := svc.SetInitialPassword(updateTestPassword, "PAD", ""); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	srv := NewServer(svc, testLog())
	cookie, csrf := loginAs(t, srv, updateTestPassword)

	rec := postForm(t, srv.Handler(), "/actions/update/apply", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusFound {
		t.Fatalf("nil-safe apply: want 302 (graceful), got %d body=%s", rec.Code, rec.Body.String())
	}
	assertApplyNotCalled(t, applyCh)
}
