package web

import (
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
)

// newTestServer wires a Server to a saved, otherwise-valid config (origin
// and Darwin token already present, as validCfg does for the Service tests)
// with no admin password yet, so setupGate redirects everything to /setup.
// Because a token is already stored, /setup's token field can be left blank
// in these tests without tripping config.Validate's darwin.token requirement
// (the "keep the stored secret" write-only path). A literally virgin device
// — no config file, no stored token — is covered by newTestServerVirgin
// below and by TestSetInitialPasswordVirginDevice in service_test.go.
func newTestServer(t *testing.T) (*Server, *Service) {
	t.Helper()
	svc, _ := newTestService(t, validCfg())
	return NewServer(svc, testLog()), svc
}

// newTestServerVirgin wires a Server to a config path with no file on it at
// all — a genuinely virgin device, with no stored Darwin token to fall back
// on. Unlike newTestServer, POSTing /setup here with a blank token must be
// rejected by config.Validate (darwin.token is required), since there is
// nothing to keep.
func newTestServerVirgin(t *testing.T) (*Server, *Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	svc := newTestServiceAt(t, path)
	return NewServer(svc, testLog()), svc
}

// newTestServerWithPassword is newTestServer plus a completed first-boot
// setup, so callers land straight in "setup done, needs login" state.
func newTestServerWithPassword(t *testing.T, pw string) (*Server, *Service) {
	t.Helper()
	srv, svc := newTestServer(t)
	if err := svc.SetInitialPassword(pw, "PAD", "tok-test"); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	return srv, svc
}

// newTestServerWithApply is newTestServer plus a channel that receives a
// value whenever this Server's wired Actions.Apply fires — used by the
// setup-flow tests that must assert POST /setup's success path actually
// schedules the same apply-by-restart a config sub-page save uses (newTestServer's
// underlying newTestServiceAt wires a no-op Apply, which cannot prove that).
func newTestServerWithApply(t *testing.T) (*Server, *Service, chan struct{}) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, validCfg()); err != nil {
		t.Fatal(err)
	}
	applyCh := make(chan struct{}, 1)
	src := Sources{
		Snapshot:  func() *board.Snapshot { return nil },
		Ring:      obs.NewRing(8),
		Version:   "vtest",
		StartedAt: time.Now(),
	}
	act := Actions{
		Apply:  func() { applyCh <- struct{}{} },
		Reboot: func() error { return nil },
	}
	svc := NewService(path, src, act, testLog())
	return NewServer(svc, testLog()), svc, applyCh
}

// postForm issues a POST with an application/x-www-form-urlencoded body,
// through the full Handler() middleware stack.
func postForm(t *testing.T, h http.Handler, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getPath(t *testing.T, h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// loginAs logs in against a server that already has pw set, and returns the
// session cookie and that session's CSRF token, ready for authed requests.
func loginAs(t *testing.T, srv *Server, pw string) (*http.Cookie, string) {
	t.Helper()
	rec := postForm(t, srv.Handler(), "/login", url.Values{"password": {pw}})
	if rec.Code != http.StatusFound {
		t.Fatalf("login: want 302, got %d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login: want 1 cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	csrf, ok := srv.sessions.Lookup(cookie.Value)
	if !ok {
		t.Fatal("login: session not found in store after login")
	}
	return cookie, csrf
}

// (a) fresh service (no password) redirects everything but /setup and
// /static/ to /setup.
func TestServerSetupGateRedirectsWhenNoPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	for _, path := range []string{"/", "/login"} {
		rec := getPath(t, h, path)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/setup" {
			t.Fatalf("%s: want 302 /setup, got %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// (b) POST /setup with password+confirm+origin creates the password (usable
// via VerifyLogin), issues a session cookie, schedules Actions.Apply (the
// same apply-by-restart a config sub-page save uses — this is what actually clears
// a virgin device's E04 fault screen, since runConfigErrorLoop has no
// poller), renders the restart page instead of redirecting to /, and /setup
// then 404s.
func TestServerSetupPostCreatesPasswordAndSession(t *testing.T) {
	srv, svc, applyCh := newTestServerWithApply(t)
	h := srv.Handler()

	form := url.Values{"password": {"longenough1"}, "confirm": {"longenough1"}, "origin": {"pad"}}
	rec := postForm(t, h, "/setup", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 restart page, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "restarting") {
		t.Fatalf("expected restart copy in body: %s", rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 1 {
		t.Fatalf("expected exactly one Set-Cookie, got %d", len(rec.Result().Cookies()))
	}
	if !svc.VerifyLogin("longenough1") {
		t.Fatal("password was not stored by /setup")
	}
	awaitApply(t, applyCh)

	rec2 := getPath(t, h, "/setup")
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("want 404 for /setup once a password exists, got %d", rec2.Code)
	}
}

// (b2) on a genuinely virgin device (no config file, no stored token),
// POSTing /setup with no token must re-render 200 with an error surfacing
// config.Validate's darwin.token rejection — not a 500, and not a redirect
// to / as if setup had succeeded.
func TestServerSetupPostVirginDeviceRequiresToken(t *testing.T) {
	srv, svc := newTestServerVirgin(t)
	h := srv.Handler()

	form := url.Values{"password": {"longenough1"}, "confirm": {"longenough1"}, "origin": {"PAD"}}
	rec := postForm(t, h, "/setup", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "darwin.token") {
		t.Fatalf("expected darwin.token error in body: %s", rec.Body.String())
	}
	if svc.VerifyLogin("longenough1") {
		t.Fatal("password must not be set when setup is rejected for a missing token")
	}
}

// (c) mismatched confirm re-renders the form with an error and leaves no
// password set.
func TestServerSetupPostMismatchedConfirm(t *testing.T) {
	srv, svc := newTestServer(t)
	h := srv.Handler()

	form := url.Values{"password": {"longenough1"}, "confirm": {"different1"}, "origin": {"PAD"}}
	rec := postForm(t, h, "/setup", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "class=\"error\"") {
		t.Fatalf("expected error markup in body: %s", rec.Body.String())
	}
	if svc.VerifyLogin("longenough1") {
		t.Fatal("password must not be set when confirm mismatches")
	}
}

// (d) with a password set: unauthenticated GET / redirects to /login; a
// correct POST /login sets a hardened cookie and redirects to /; a wrong
// password re-renders 200 with a generic error.
func TestServerLoginFlow(t *testing.T) {
	srv, _ := newTestServerWithPassword(t, "longenough1")
	h := srv.Handler()

	rec := getPath(t, h, "/")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	recOK := postForm(t, h, "/login", url.Values{"password": {"longenough1"}})
	if recOK.Code != http.StatusFound || recOK.Header().Get("Location") != "/" {
		t.Fatalf("want 302 /, got %d %q", recOK.Code, recOK.Header().Get("Location"))
	}
	cookies := recOK.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if !c.HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie SameSite = %v, want Strict", c.SameSite)
	}
	if c.MaxAge <= 0 {
		t.Fatalf("session cookie MaxAge = %d, want > 0", c.MaxAge)
	}

	recBad := postForm(t, h, "/login", url.Values{"password": {"wrongpassword"}})
	if recBad.Code != http.StatusOK {
		t.Fatalf("want 200 re-render on bad password, got %d", recBad.Code)
	}
	if !strings.Contains(recBad.Body.String(), "incorrect") {
		t.Fatalf("expected generic 'incorrect' message, got: %s", recBad.Body.String())
	}
}

// (e) two successful logins yield different session tokens (rotation).
func TestServerLoginRotatesSessionToken(t *testing.T) {
	srv, _ := newTestServerWithPassword(t, "longenough1")
	h := srv.Handler()

	rec1 := postForm(t, h, "/login", url.Values{"password": {"longenough1"}})
	rec2 := postForm(t, h, "/login", url.Values{"password": {"longenough1"}})
	tok1 := rec1.Result().Cookies()[0].Value
	tok2 := rec2.Result().Cookies()[0].Value
	if tok1 == tok2 {
		t.Fatal("expected a fresh session token on every successful login")
	}
}

// (f) POST /logout (with the session's CSRF token) destroys the session;
// afterwards GET / redirects to /login again.
func TestServerLogoutDestroysSession(t *testing.T) {
	srv, _ := newTestServerWithPassword(t, "longenough1")
	h := srv.Handler()
	cookie, csrf := loginAs(t, srv, "longenough1")

	rec := postForm(t, h, "/logout", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec2 := getPath(t, h, "/", cookie)
	if rec2.Code != http.StatusFound || rec2.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login after logout, got %d %q", rec2.Code, rec2.Header().Get("Location"))
	}
}

// (g) six rapid POST /login attempts trip the auth rate limiter (burst 5) on
// the sixth.
func TestServerLoginRateLimited(t *testing.T) {
	srv, _ := newTestServerWithPassword(t, "longenough1")
	h := srv.Handler()

	var lastCode int
	for i := 0; i < 6; i++ {
		rec := postForm(t, h, "/login", url.Values{"password": {"wrongpassword"}})
		lastCode = rec.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("want 429 on the 6th rapid attempt, got %d", lastCode)
	}
}

// (h) /static/style.css is served without authentication, even pre-setup.
func TestServerStaticServesWithoutAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := getPath(t, srv.Handler(), "/static/style.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

// TestStaticFontsServed confirms the Wayfinding design system's subset
// woff2 fonts land under static/fonts/ and are picked up automatically by
// the //go:embed templates/* static/* directive, with no route wiring
// needed beyond the existing GET /static/ file server.
func TestStaticFontsServed(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	for _, path := range []string{
		"/static/fonts/rail-alphabet-dark.woff2",
		"/static/fonts/rail-alphabet-light.woff2",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: want 200, got %d", path, rec.Code)
		}
		if rec.Body.Len() < 1000 {
			t.Errorf("GET %s: suspiciously small body (%d bytes)", path, rec.Body.Len())
		}
	}
}

// TestStaticBoardJSServed confirms board.js (Task 4's client-side board
// renderer) is served, without auth, the same way as style.css and the
// fonts — picked up automatically by //go:embed templates/* static/*, no
// dedicated route.
func TestStaticBoardJSServed(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := getPath(t, srv.Handler(), "/static/board.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/api/board") {
		t.Fatalf("board.js body does not reference /api/board: %s", rec.Body.String())
	}
}
