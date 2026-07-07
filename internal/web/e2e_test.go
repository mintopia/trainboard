package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

// --- Test 1: the full ten-step journey -------------------------------------

// e2ePassword is the admin password minted during this journey's /setup step.
const e2ePassword = "longenough1"

// e2eNewService wires a Service to an already-saved config at path, with its
// own Sources/Actions and an Apply-notification channel — used both for the
// journey's initial Server and, at step 5, for a second Service standing in
// for a freshly-restarted process over the same on-disk config.
func e2eNewService(t *testing.T, path string) (*Service, chan struct{}) {
	t.Helper()
	applyCh := make(chan struct{}, 1)
	src := Sources{
		Snapshot:   func() *board.Snapshot { return nil },
		Ring:       obs.NewRing(8),
		PreviewPNG: func() []byte { return nil },
		Version:    "v-e2e",
		StartedAt:  time.Now(),
	}
	act := Actions{
		Apply:  func() { applyCh <- struct{}{} },
		Reboot: func() error { return nil },
	}
	return NewService(path, src, act, testLog()), applyCh
}

// TestE2EFullJourney drives the whole admin UI end to end over a real Service
// backed by a real temp config file and real Sessions, through the full
// Handler() middleware stack, exactly as an operator (and an attacker) would
// see it. Each numbered step below matches the journey in the task-11 brief.
func TestE2EFullJourney(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	cfg.Board.Origin = "PAD"
	cfg.Darwin.Token = "tok-original"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	svc, applyCh := e2eNewService(t, path)
	srv := NewServer(svc, testLog())
	h := srv.Handler()

	// 1. Fresh config (no password): / -> 302 /setup, and /api/status -> 302
	// too. This pins the actual, deliberate behaviour: setupGate runs in the
	// OUTER middleware chain (Handler()), entirely before mux dispatch, so it
	// never reaches requireAuth/apiJSONErrors — the first-boot gate outranks
	// the API's usual 401 JSON contract. An API client hitting a virgin
	// device gets an HTML redirect, not JSON, until setup completes.
	if rec := getPath(t, h, "/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("step1 GET /: want 302 /setup, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := getPath(t, h, "/api/status"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("step1 GET /api/status: want 302 /setup (setup gate outranks API auth), got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// 2. POST /setup (password+origin, blank token keeps the stored one) ->
	// 200 restart page with a session issued and Actions.Apply scheduled,
	// exactly like handleConfigPost's apply-by-restart — this Service's Apply
	// is a channel send rather than a real os.Exit, so the session created
	// here stays valid for step 3 instead of the process actually restarting;
	// /setup then 404s.
	setupForm := url.Values{"password": {e2ePassword}, "confirm": {e2ePassword}, "origin": {"PAD"}, "token": {""}}
	recSetup := postForm(t, h, "/setup", setupForm)
	if recSetup.Code != http.StatusOK {
		t.Fatalf("step2 POST /setup: want 200 restart page, got %d body=%s", recSetup.Code, recSetup.Body.String())
	}
	awaitApply(t, applyCh)
	cookies := recSetup.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("step2: want exactly 1 Set-Cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if rec := getPath(t, h, "/setup"); rec.Code != http.StatusNotFound {
		t.Fatalf("step2: want 404 for /setup once a password exists, got %d", rec.Code)
	}

	// 3. GET / -> 200; GET /api/status with the cookie -> 200.
	if rec := getPath(t, h, "/", cookie); rec.Code != http.StatusOK {
		t.Fatalf("step3 GET /: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := getPath(t, h, "/api/status", cookie); rec.Code != http.StatusOK {
		t.Fatalf("step3 GET /api/status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	csrf, ok := srv.sessions.Lookup(cookie.Value)
	if !ok {
		t.Fatal("step3: session not found in store after setup")
	}

	// 4. POST /config changing refresh + setting Darwin token "tok-e2e" ->
	// applied; Apply fired.
	cfgForm := baseConfigForm()
	cfgForm.Set("board.refreshSeconds", "90")
	cfgForm.Set("darwin.token", "tok-e2e")
	cfgForm.Set("csrf", csrf)
	recCfg := postForm(t, h, "/config", cfgForm, cookie)
	if recCfg.Code != http.StatusOK {
		t.Fatalf("step4 POST /config: want 200, got %d body=%s", recCfg.Code, recCfg.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatalf("step4: reload config: %v", err)
	}
	if cur.Board.RefreshSeconds != 90 {
		t.Fatalf("step4: board.refreshSeconds = %d, want 90", cur.Board.RefreshSeconds)
	}
	if cur.Darwin.Token != "tok-e2e" {
		t.Fatalf("step4: darwin.token = %q, want %q", cur.Darwin.Token, "tok-e2e")
	}

	// 5. Re-login (restart simulation): a brand new Service and Server over
	// the SAME temp config path — standing in for a systemd restart after the
	// config save above, since the in-memory Sessions store (and this test's
	// fake Service) would not otherwise survive a real process restart. The
	// old cookie must be dead against the new Sessions store; login with the
	// same password must still work, proving the password itself persisted
	// on disk across the "restart".
	svc2, _ := e2eNewService(t, path) // no config-changing calls follow, so this Service's own Apply channel is never exercised
	srv2 := NewServer(svc2, testLog())
	h2 := srv2.Handler()

	if rec := getPath(t, h2, "/", cookie); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("step5: old cookie should be dead after restart, want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cookie2, csrf2 := loginAs(t, srv2, e2ePassword)

	// 6. The secrets sweep: none of the five authed GET surfaces may ever
	// render the raw Darwin token.
	sweepPaths := []string{"/config", "/api/config", "/api/status", "/api/events", "/"}
	for _, p := range sweepPaths {
		rec := getPath(t, h2, p, cookie2)
		if rec.Code != http.StatusOK {
			t.Fatalf("step6 GET %s: want 200, got %d body=%s", p, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "tok-e2e") {
			t.Fatalf("step6: Darwin token leaked via %s: %s", p, rec.Body.String())
		}
	}

	// 7. CSRF negative: POST /config with a valid session but the wrong csrf
	// token -> 403, file unchanged.
	beforeCSRF, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	badCSRFForm := baseConfigForm()
	badCSRFForm.Set("darwin.token", "tok-e2e") // keep matching current stored value; blank would too, but be explicit
	badCSRFForm.Set("csrf", "wrong-csrf-token")
	recBadCSRF := postForm(t, h2, "/config", badCSRFForm, cookie2)
	if recBadCSRF.Code != http.StatusForbidden {
		t.Fatalf("step7: want 403 on bad csrf, got %d body=%s", recBadCSRF.Code, recBadCSRF.Body.String())
	}
	afterCSRF, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterCSRF, beforeCSRF) {
		t.Fatalf("step7: config file must be unchanged on CSRF rejection:\nbefore=%+v\nafter=%+v", beforeCSRF, afterCSRF)
	}

	// 8. Origin negative: POST with a foreign Origin header (even carrying a
	// otherwise-valid CSRF token) -> 403. originCheck runs in the outer
	// middleware chain, ahead of csrfProtect, so this is rejected before the
	// CSRF token is even inspected.
	originForm := baseConfigForm()
	originForm.Set("csrf", csrf2)
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(originForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	req.AddCookie(cookie2)
	recOrigin := httptest.NewRecorder()
	h2.ServeHTTP(recOrigin, req)
	if recOrigin.Code != http.StatusForbidden {
		t.Fatalf("step8: want 403 on foreign Origin, got %d body=%s", recOrigin.Code, recOrigin.Body.String())
	}

	// 9. Rate limit: 6 rapid POST /login attempts with the wrong password
	// trip the auth limiter (burst 5) by the 6th.
	var lastLoginCode int
	for i := 0; i < 6; i++ {
		rec := postForm(t, h2, "/login", url.Values{"password": {"definitely-wrong"}})
		lastLoginCode = rec.Code
	}
	if lastLoginCode != http.StatusTooManyRequests {
		t.Fatalf("step9: want 429 on the 6th rapid wrong-password attempt, got %d", lastLoginCode)
	}

	// 10. Logout -> protected routes 302 again.
	recLogout := postForm(t, h2, "/logout", url.Values{"csrf": {csrf2}}, cookie2)
	if recLogout.Code != http.StatusFound || recLogout.Header().Get("Location") != "/login" {
		t.Fatalf("step10 POST /logout: want 302 /login, got %d %q", recLogout.Code, recLogout.Header().Get("Location"))
	}
	if rec := getPath(t, h2, "/", cookie2); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("step10: want 302 /login after logout, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// --- Test 2: the security invariant matrix ---------------------------------

// routeCase is one row of the route matrix: a single registered route,
// exercised both with no session and with a valid session.
type routeCase struct {
	// name identifies the route in subtest output, e.g. "GET /config".
	name string
	// method and path are the exact registration from server.go's mux table.
	method, path string
	// isAPI marks a /api/* route: unauthenticated requests get 401 JSON
	// instead of a 302 HTML redirect (spec invariant 1).
	isAPI bool
	// body builds this route's request body/headers for a *successful*
	// authenticated call, given that session's CSRF token: (reader,
	// Content-Type, X-CSRF-Token header value). GET routes need none of it.
	body func(csrf string) (io.Reader, string, string)
	// wantAuthedStatus/wantAuthedLoc are the expected outcome of a fully
	// authenticated, well-formed call — proof the route is reachable once
	// logged in, not just that it is blocked when logged out.
	wantAuthedStatus int
	wantAuthedLoc    string
	// appliesAsync is true if a successful authed call schedules
	// Actions.Apply, so the test must drain applyCh before moving on (the
	// channel is buffered 1; leaving it undrained would leak the AfterFunc
	// goroutine into the next case's send).
	appliesAsync bool
}

// noBody is the request-builder for every GET route in the matrix.
func noBody(_ string) (io.Reader, string, string) { return nil, "", "" }

// htmlForm builds an application/x-www-form-urlencoded body carrying vals
// plus the given CSRF token in the "csrf" field, matching how every HTML
// state-changing route in this codebase expects its CSRF token.
func htmlForm(vals url.Values) func(csrf string) (io.Reader, string, string) {
	return func(csrf string) (io.Reader, string, string) {
		v := url.Values{}
		for k, vv := range vals {
			v[k] = vv
		}
		v.Set("csrf", csrf)
		return strings.NewReader(v.Encode()), "application/x-www-form-urlencoded", ""
	}
}

// apiBody builds a JSON request body carrying the CSRF token in the
// X-CSRF-Token header, matching how every /api/* state-changing route
// expects its CSRF token instead.
func apiBody(payload []byte) func(csrf string) (io.Reader, string, string) {
	return func(csrf string) (io.Reader, string, string) {
		var r io.Reader
		if payload != nil {
			r = bytes.NewReader(payload)
		}
		return r, "application/json", csrf
	}
}

// TestRouteSecurityInvariantMatrix is the tripwire test: it enumerates EVERY
// route NewServer registers except /setup, /login, and /static/* (those
// three are governed by the setup-gate/no-auth exception in spec invariant
// 1, not by session auth — see TestServerSetupGateRedirectsWhenNoPassword and
// TestServerStaticServesWithoutAuth for their own coverage). Each route is
// checked twice: with no session (must be blocked — 302 for HTML, 401 JSON
// for /api/*) and with a valid session (must succeed with its normal,
// documented response).
//
// *** Maintainers: when NewServer gains a new route, add a row here. ***
// This table is what catches an unprotected route slipping in unnoticed.
//
//	Route                          | method | no session   | valid session
//	-------------------------------|--------|--------------|----------------
//	/                              | GET    | 302 /login   | 200
//	/preview.png                   | GET    | 302 /login   | 404 (no preview wired in this harness)
//	/events                        | GET    | 302 /login   | 200
//	/config                        | GET    | 302 /login   | 200
//	/config                        | POST   | 302 /login   | 200
//	/config/ap-password            | POST   | 302 /login   | 200
//	/actions                       | GET    | 302 /login   | 200
//	/actions/restart               | POST   | 302 /login   | 200
//	/actions/reboot                | POST   | 302 /login   | 200
//	/api/status                    | GET    | 401 JSON     | 200
//	/api/config                    | GET    | 401 JSON     | 200
//	/api/config                    | PUT    | 401 JSON     | 200
//	/api/events                    | GET    | 401 JSON     | 200
//	/api/actions/restart           | POST   | 401 JSON     | 200
//	/api/actions/reboot            | POST   | 401 JSON     | 200
//	/logout                        | POST   | 302 /login   | 302 /login (destroys session; kept LAST)
func TestRouteSecurityInvariantMatrix(t *testing.T) {
	srv, _, _, applyCh := newConfigTestServer(t)
	h := srv.Handler()

	cfgBody := config.Default()
	cfgBody.Board.Origin = "PAD"
	apiCfgPayload, err := json.Marshal(configUpdateJSON{Config: cfgBody})
	if err != nil {
		t.Fatalf("marshal api config payload: %v", err)
	}

	// routeMatrix must list every route server.go registers, in the exact
	// method+path form used there, other than /setup, /login, /static/*.
	// POST /logout MUST stay last: it destroys the shared session created
	// below, and every other row's "valid session" arm depends on that
	// session still being alive.
	routeMatrix := []routeCase{
		{name: "GET /", method: http.MethodGet, path: "/", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "GET /preview.png", method: http.MethodGet, path: "/preview.png", body: noBody, wantAuthedStatus: http.StatusNotFound},
		{name: "GET /events", method: http.MethodGet, path: "/events", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "GET /config", method: http.MethodGet, path: "/config", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /config", method: http.MethodPost, path: "/config", body: htmlForm(baseConfigForm()), wantAuthedStatus: http.StatusOK, appliesAsync: true},
		{name: "POST /config/ap-password", method: http.MethodPost, path: "/config/ap-password", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusOK},
		{name: "GET /actions", method: http.MethodGet, path: "/actions", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /actions/restart", method: http.MethodPost, path: "/actions/restart", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusOK, appliesAsync: true},
		{name: "POST /actions/reboot", method: http.MethodPost, path: "/actions/reboot", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusOK},
		{name: "GET /api/status", method: http.MethodGet, path: "/api/status", isAPI: true, body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "GET /api/config", method: http.MethodGet, path: "/api/config", isAPI: true, body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "PUT /api/config", method: http.MethodPut, path: "/api/config", isAPI: true, body: apiBody(apiCfgPayload), wantAuthedStatus: http.StatusOK, appliesAsync: true},
		{name: "GET /api/events", method: http.MethodGet, path: "/api/events", isAPI: true, body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /api/actions/restart", method: http.MethodPost, path: "/api/actions/restart", isAPI: true, body: apiBody(nil), wantAuthedStatus: http.StatusOK, appliesAsync: true},
		{name: "POST /api/actions/reboot", method: http.MethodPost, path: "/api/actions/reboot", isAPI: true, body: apiBody(nil), wantAuthedStatus: http.StatusOK},
		{name: "POST /logout", method: http.MethodPost, path: "/logout", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusFound, wantAuthedLoc: "/login"},
	}

	// One shared session for the whole "valid session" arm: logging in fresh
	// per row would blow through the auth limiter's burst-of-5 long before
	// reaching the end of a 16-row table.
	cookie, csrf := loginAs(t, srv, configTestPassword)

	for _, tc := range routeMatrix {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("no session", func(t *testing.T) {
				body, ct, csrfHdr := tc.body("unused-no-session")
				req := httptest.NewRequest(tc.method, tc.path, body)
				if ct != "" {
					req.Header.Set("Content-Type", ct)
				}
				if csrfHdr != "" {
					req.Header.Set("X-CSRF-Token", csrfHdr)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				if tc.isAPI {
					if rec.Code != http.StatusUnauthorized {
						t.Fatalf("no session: want 401, got %d body=%s", rec.Code, rec.Body.String())
					}
					if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
						t.Fatalf("no session: want application/json, got %q", ct)
					}
				} else {
					if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
						t.Fatalf("no session: want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
					}
				}
			})

			t.Run("valid session", func(t *testing.T) {
				body, ct, csrfHdr := tc.body(csrf)
				req := httptest.NewRequest(tc.method, tc.path, body)
				if ct != "" {
					req.Header.Set("Content-Type", ct)
				}
				if csrfHdr != "" {
					req.Header.Set("X-CSRF-Token", csrfHdr)
				}
				req.AddCookie(cookie)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				if rec.Code != tc.wantAuthedStatus {
					t.Fatalf("valid session: want %d, got %d body=%s", tc.wantAuthedStatus, rec.Code, rec.Body.String())
				}
				if tc.wantAuthedLoc != "" && rec.Header().Get("Location") != tc.wantAuthedLoc {
					t.Fatalf("valid session: want Location %q, got %q", tc.wantAuthedLoc, rec.Header().Get("Location"))
				}
				if tc.appliesAsync {
					awaitApply(t, applyCh)
				}
			})
		})
	}
}
