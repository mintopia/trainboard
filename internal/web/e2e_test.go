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
		Snapshot:  func() *board.Snapshot { return nil },
		Ring:      obs.NewRing(8),
		Version:   "v-e2e",
		StartedAt: time.Now(),
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
	// 200 "setupDone" (the route-line-aware wait page, not the generic
	// /restarting interstitial) with a session issued and Actions.Apply
	// scheduled, exactly like a config sub-page save's apply-by-restart —
	// this Service's Apply is a channel send rather than a real os.Exit, so
	// the session created here stays valid for step 3 instead of the
	// process actually restarting; /setup then 404s.
	setupForm := url.Values{"password": {e2ePassword}, "confirm": {e2ePassword}, "origin": {"PAD"}, "token": {""}}
	recSetup := postForm(t, h, "/setup", setupForm)
	if recSetup.Code != http.StatusOK {
		t.Fatalf("step2 POST /setup: want 200 setupDone, got %d body=%s", recSetup.Code, recSetup.Body.String())
	}
	if !strings.Contains(recSetup.Body.String(), "Departures live") {
		t.Fatalf("step2: expected route-line setup-done copy in body: %s", recSetup.Body.String())
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

	// 4a. POST /config/departures changing refresh -> 303, Apply fired. This
	// is the new per-section route (this task); board.origin is resubmitted
	// unchanged (PAD) since the departures form owns the whole board.*
	// fieldset, not just refreshSeconds.
	depForm := baseDeparturesForm()
	depForm.Set("board.refreshSeconds", "90")
	depForm.Set("csrf", csrf)
	recDep := postForm(t, h, "/config/departures", depForm, cookie)
	if recDep.Code != http.StatusSeeOther {
		t.Fatalf("step4a POST /config/departures: want 303, got %d body=%s", recDep.Code, recDep.Body.String())
	}
	awaitApply(t, applyCh)

	// 4b. POST /config/network setting Darwin token "tok-e2e" -> 303, Apply
	// fired. darwin.token now lives on its own sub-page (this task).
	netForm := baseNetworkForm()
	netForm.Set("darwin.token", "tok-e2e")
	netForm.Set("csrf", csrf)
	recCfg := postForm(t, h, "/config/network", netForm, cookie)
	if recCfg.Code != http.StatusSeeOther {
		t.Fatalf("step4b POST /config/network: want 303, got %d body=%s", recCfg.Code, recCfg.Body.String())
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

	// 7. CSRF negative: POST /config/network with a valid session but the
	// wrong csrf token -> 403, file unchanged.
	beforeCSRF, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	badCSRFForm := baseNetworkForm()
	badCSRFForm.Set("darwin.token", "tok-e2e") // keep matching current stored value; blank would too, but be explicit
	badCSRFForm.Set("csrf", "wrong-csrf-token")
	recBadCSRF := postForm(t, h2, "/config/network", badCSRFForm, cookie2)
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
	originForm := baseNetworkForm()
	originForm.Set("csrf", csrf2)
	req := httptest.NewRequest(http.MethodPost, "/config/network", strings.NewReader(originForm.Encode()))
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
// route NewServer registers except /setup, /login, /static/*, GET /restarting,
// and the three captive-portal probe endpoints (/generate_204,
// /hotspot-detect.html, /ncsi.txt) — all seven are governed by the
// setup-gate/no-auth exception in spec invariant 1, not by session auth.
// /setup, /login, /static/* have their own coverage in
// TestServerSetupGateRedirectsWhenNoPassword and
// TestServerStaticServesWithoutAuth; GET /restarting is deliberately public
// (a restart-triggering save's browser may have lost its session by the time
// it lands there) and is pinned by TestRestartingRendersWithoutSession
// (handlers_actions_test.go); the three probes are deliberately pre-auth AND
// pre-CSRF by design — a just-associated phone has no session and never will
// until a human deliberately visits /setup — and are pinned instead by
// TestPortalProbeMatrix (handlers_portal_test.go), whose two arms (AP mode /
// not AP mode) play the role this table's (no session / valid session) arms
// play here. Each route is
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
//	/events                        | GET    | 302 /login   | 200
//	/config                        | GET    | 302 /login   | 200
//	/config/departures             | GET    | 302 /login   | 200
//	/config/departures             | POST   | 302 /login   | 303 /restarting
//	/config/display                | GET    | 302 /login   | 200
//	/config/display                | POST   | 302 /login   | 303 /restarting
//	/actions                       | GET    | 302 /login   | 200
//	/actions/restart               | POST   | 302 /login   | 303 /restarting
//	/actions/reboot                | POST   | 302 /login   | 200
//	/actions/soak                  | POST   | 302 /login   | 302 /actions
//	/actions/soak/cancel           | POST   | 302 /login   | 302 /actions
//	/actions/wifi-retry            | POST   | 302 /login   | 302 /actions
//	/api/status                    | GET    | 401 JSON     | 200
//	/api/config                    | GET    | 401 JSON     | 200
//	/api/config                    | PUT    | 401 JSON     | 200
//	/api/events                    | GET    | 401 JSON     | 200
//	/api/actions/restart           | POST   | 401 JSON     | 200
//	/api/actions/reboot            | POST   | 401 JSON     | 200
//	/api/actions/soak              | POST   | 401 JSON     | 200
//	/api/actions/soak/cancel       | POST   | 401 JSON     | 200
//	/api/actions/wifi-retry        | POST   | 401 JSON     | 200
//	/logout                        | POST   | 302 /login   | 302 /login (destroys session; kept LAST)
//
// The HTML monolith's POST /config is GONE (task 7 — see
// TestOldMonolithConfigPostGone in handlers_config_test.go), so there is no
// row for it any more. Its replacement per-section routes
// (/config/{network,updates,admin}) are NOT rows in this table either — see
// TestRouteSecurityInvariantMatrixConfigSectionRoutes below, alongside
// task 13's five update routes, which check them against their own Servers
// so their extra requests don't blow this table's shared actionLimit budget
// (see that test's doc comment for why).
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
		{name: "GET /events", method: http.MethodGet, path: "/events", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "GET /config", method: http.MethodGet, path: "/config", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "GET /config/departures", method: http.MethodGet, path: "/config/departures", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /config/departures", method: http.MethodPost, path: "/config/departures", body: htmlForm(baseDeparturesForm()), wantAuthedStatus: http.StatusSeeOther, wantAuthedLoc: "/restarting", appliesAsync: true},
		{name: "GET /config/display", method: http.MethodGet, path: "/config/display", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /config/display", method: http.MethodPost, path: "/config/display", body: htmlForm(baseDisplayForm()), wantAuthedStatus: http.StatusSeeOther, wantAuthedLoc: "/restarting", appliesAsync: true},
		{name: "GET /actions", method: http.MethodGet, path: "/actions", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /actions/restart", method: http.MethodPost, path: "/actions/restart", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusSeeOther, wantAuthedLoc: "/restarting", appliesAsync: true},
		{name: "POST /actions/reboot", method: http.MethodPost, path: "/actions/reboot", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusOK},
		{name: "POST /actions/soak", method: http.MethodPost, path: "/actions/soak", body: htmlForm(url.Values{"duration": {"1h"}}), wantAuthedStatus: http.StatusFound, wantAuthedLoc: "/actions"},
		{name: "POST /actions/soak/cancel", method: http.MethodPost, path: "/actions/soak/cancel", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusFound, wantAuthedLoc: "/actions"},
		{name: "POST /actions/wifi-retry", method: http.MethodPost, path: "/actions/wifi-retry", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusFound, wantAuthedLoc: "/actions"},
		{name: "GET /api/status", method: http.MethodGet, path: "/api/status", isAPI: true, body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "GET /api/config", method: http.MethodGet, path: "/api/config", isAPI: true, body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "PUT /api/config", method: http.MethodPut, path: "/api/config", isAPI: true, body: apiBody(apiCfgPayload), wantAuthedStatus: http.StatusOK, appliesAsync: true},
		{name: "GET /api/events", method: http.MethodGet, path: "/api/events", isAPI: true, body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /api/actions/restart", method: http.MethodPost, path: "/api/actions/restart", isAPI: true, body: apiBody(nil), wantAuthedStatus: http.StatusOK, appliesAsync: true},
		{name: "POST /api/actions/reboot", method: http.MethodPost, path: "/api/actions/reboot", isAPI: true, body: apiBody(nil), wantAuthedStatus: http.StatusOK},
		{name: "POST /api/actions/soak", method: http.MethodPost, path: "/api/actions/soak", isAPI: true, body: apiBody([]byte(`{"duration":"1h"}`)), wantAuthedStatus: http.StatusOK},
		{name: "POST /api/actions/soak/cancel", method: http.MethodPost, path: "/api/actions/soak/cancel", isAPI: true, body: apiBody(nil), wantAuthedStatus: http.StatusOK},
		{name: "POST /api/actions/wifi-retry", method: http.MethodPost, path: "/api/actions/wifi-retry", isAPI: true, body: apiBody(nil), wantAuthedStatus: http.StatusOK},
		{name: "POST /logout", method: http.MethodPost, path: "/logout", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusFound, wantAuthedLoc: "/login"},
	}

	// One shared session for the whole "valid session" arm: logging in fresh
	// per row would blow through the auth limiter's burst-of-5 long before
	// reaching the end of a 16-row table.
	cookie, csrf := loginAs(t, srv, configTestPassword)

	for _, tc := range routeMatrix {
		runRouteCase(t, h, tc, cookie, csrf, applyCh)
	}
}

// runRouteCase exercises a single routeMatrix row's two arms (no session /
// valid session) against h, using cookie/csrf for the authed arm. Shared by
// TestRouteSecurityInvariantMatrix and TestRouteSecurityInvariantMatrixUpdateRoutes
// so both tables are checked identically.
func runRouteCase(t *testing.T, h http.Handler, tc routeCase, cookie *http.Cookie, csrf string, applyCh chan struct{}) {
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

// TestRouteSecurityInvariantMatrixUpdateRoutes covers task 13's five new
// routes (/actions/update/{check,apply,dismiss} and
// /api/actions/update/{check,apply}) with the exact same two-arm check as
// TestRouteSecurityInvariantMatrix. It runs against its own fresh Server
// (and therefore its own actionLimit bucket) rather than joining the main
// table: the main table's shared session already spends ~26 of the 30
// actionLimit tokens across its 13 rate-limited rows, and this table's own
// 10 requests (5 rows x 2 arms) would tip it over the burst and start
// failing with 429s instead of the responses under test.
//
//	Route                          | method | no session   | valid session
//	-------------------------------|--------|--------------|----------------
//	/actions/update/check          | POST   | 302 /login   | 302 /?checked=1 (Update seams unwired in this harness; Service.CheckForUpdate's nil-safe error still redirects)
//	/actions/update/apply          | POST   | 302 /login   | 302 / (nil-safe error path; no restart scheduled — see handlers_update_test.go for the wired-seam success/failure behaviour)
//	/actions/update/dismiss        | POST   | 302 /login   | 302 / (Service.DismissRollback's nil-safe no-op still redirects)
//	/api/actions/update/check      | POST   | 401 JSON     | 500 JSON (nil-safe "not available" error, uniform error shape — see handlers_update_test.go for the wired-seam success path)
//	/api/actions/update/apply      | POST   | 401 JSON     | 500 JSON (nil-safe error; no restart scheduled)
func TestRouteSecurityInvariantMatrixUpdateRoutes(t *testing.T) {
	srv, _, _, applyCh := newConfigTestServer(t)
	h := srv.Handler()
	cookie, csrf := loginAs(t, srv, configTestPassword)

	updateRouteMatrix := []routeCase{
		{name: "POST /actions/update/check", method: http.MethodPost, path: "/actions/update/check", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusFound, wantAuthedLoc: "/?checked=1"},
		{name: "POST /actions/update/apply", method: http.MethodPost, path: "/actions/update/apply", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusFound, wantAuthedLoc: "/"},
		{name: "POST /actions/update/dismiss", method: http.MethodPost, path: "/actions/update/dismiss", body: htmlForm(url.Values{}), wantAuthedStatus: http.StatusFound, wantAuthedLoc: "/"},
		{name: "POST /api/actions/update/check", method: http.MethodPost, path: "/api/actions/update/check", isAPI: true, body: apiBody(nil), wantAuthedStatus: http.StatusInternalServerError},
		{name: "POST /api/actions/update/apply", method: http.MethodPost, path: "/api/actions/update/apply", isAPI: true, body: apiBody(nil), wantAuthedStatus: http.StatusInternalServerError},
	}

	for _, tc := range updateRouteMatrix {
		runRouteCase(t, h, tc, cookie, csrf, applyCh)
	}
}

// TestRouteSecurityInvariantMatrixConfigSectionRoutes covers task 7's three
// new config sub-page routes (/config/{network,updates,admin}) with the same
// two-arm check as TestRouteSecurityInvariantMatrix, against its own fresh
// Server for the same reason as TestRouteSecurityInvariantMatrixUpdateRoutes:
// the main table's shared session is already close to its actionLimit
// budget. Network and Updates schedule Actions.Apply on a successful save
// (appliesAsync); Admin deliberately does not — see handleConfigAdminPost's
// doc comment.
//
//	Route                          | method | no session   | valid session
//	-------------------------------|--------|--------------|----------------
//	/config/network                | GET    | 302 /login   | 200
//	/config/network                | POST   | 302 /login   | 303 /restarting
//	/config/updates                | GET    | 302 /login   | 200
//	/config/updates                | POST   | 302 /login   | 303 /restarting
//	/config/admin                  | GET    | 302 /login   | 200
//	/config/admin                  | POST   | 302 /login   | 303 /config (no restart)
func TestRouteSecurityInvariantMatrixConfigSectionRoutes(t *testing.T) {
	srv, _, _, applyCh := newConfigTestServer(t)
	h := srv.Handler()
	cookie, csrf := loginAs(t, srv, configTestPassword)

	configSectionRouteMatrix := []routeCase{
		{name: "GET /config/network", method: http.MethodGet, path: "/config/network", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /config/network", method: http.MethodPost, path: "/config/network", body: htmlForm(baseNetworkForm()), wantAuthedStatus: http.StatusSeeOther, wantAuthedLoc: "/restarting", appliesAsync: true},
		{name: "GET /config/updates", method: http.MethodGet, path: "/config/updates", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /config/updates", method: http.MethodPost, path: "/config/updates", body: htmlForm(baseUpdatesForm()), wantAuthedStatus: http.StatusSeeOther, wantAuthedLoc: "/restarting", appliesAsync: true},
		{name: "GET /config/admin", method: http.MethodGet, path: "/config/admin", body: noBody, wantAuthedStatus: http.StatusOK},
		{name: "POST /config/admin", method: http.MethodPost, path: "/config/admin", body: htmlForm(url.Values{"web.password": {"routematrixpw1"}, "web.password.confirm": {"routematrixpw1"}}), wantAuthedStatus: http.StatusSeeOther, wantAuthedLoc: "/config"},
	}

	for _, tc := range configSectionRouteMatrix {
		runRouteCase(t, h, tc, cookie, csrf, applyCh)
	}
}

// --- Test 3: the partial-setup (AP-mode) finish-provisioning journey -------

// TestE2EPartialSetupLoginAndFinishConfig is the end-to-end guard for Gap 1:
// a device provisioned through the AP portal has an admin password + WiFi
// creds on disk but no Board.Origin/Darwin.Token yet (connectivity-valid,
// board-invalid). After it joins the LAN, the operator must be able to log in
// and reach /config to finish provisioning — the exact path config.Load's
// full validation used to break with a redirect loop. This drives that whole
// journey over the real middleware stack: setup gate lifts, login works,
// /config renders, and finishing (origin + token) promotes the document to
// fully board-valid on disk without losing the portal-supplied WiFi creds.
func TestE2EPartialSetupLoginAndFinishConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	hash, err := HashPassword(e2ePassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	partial := config.Default() // empty origin/token: fails full Validate
	partial.Web.PasswordHash = hash
	partial.Wifi.SSID = "HomeNet"
	partial.Wifi.PSK = "supersecret"
	if err := config.SaveConnectivity(path, partial); err != nil {
		t.Fatalf("seed partial-setup config: %v", err)
	}

	svc, applyCh := e2eNewService(t, path)
	srv := NewServer(svc, testLog())
	h := srv.Handler()

	// 1. Setup gate lifts: a password exists, so /setup 404s and an
	// unauthenticated request routes to /login (NOT /setup).
	if rec := getPath(t, h, "/setup"); rec.Code != http.StatusNotFound {
		t.Fatalf("partial-setup: /setup must 404 once a password exists, got %d", rec.Code)
	}
	if rec := getPath(t, h, "/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("partial-setup: GET / want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// 2. Login with the portal-set password works.
	cookie, csrf := loginAs(t, srv, e2ePassword)

	// 3. GET /config renders the settings list.
	if rec := getPath(t, h, "/config", cookie); rec.Code != http.StatusOK {
		t.Fatalf("partial-setup: GET /config want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 3b. GET /config/network shows the "Station" field, since this device
	// has no Board.Origin yet — see handleConfigNetworkPost's doc comment
	// for why this page (rather than Departures) is where first-boot
	// AP-mode provisioning finishes.
	recNetGet := getPath(t, h, "/config/network", cookie)
	if recNetGet.Code != http.StatusOK || !strings.Contains(recNetGet.Body.String(), `name="board.origin"`) {
		t.Fatalf("partial-setup: GET /config/network want 200 with a board.origin field, got %d body=%s", recNetGet.Code, recNetGet.Body.String())
	}

	// 4. Finish provisioning: a single POST /config/network supplies the
	// missing origin + token together (WiFi SSID arrives pre-filled in the
	// real form, so submit it back as-is), which promotes the document to
	// fully board-valid and saves it. Origin and token must land together in
	// one save: UpdateConfig's full Validate rejects a document with only
	// one of the two, so submitting them across two independent section
	// saves (Departures then Network, or vice versa) would fail on
	// whichever save runs first.
	form := baseNetworkForm()
	form.Set("board.origin", "PAD")
	form.Set("darwin.token", "tok-finish")
	form.Set("wifi.ssid", "HomeNet") // mirrors the pre-filled, non-secret SSID input
	form.Set("csrf", csrf)
	recCfg := postForm(t, h, "/config/network", form, cookie)
	if recCfg.Code != http.StatusSeeOther {
		t.Fatalf("partial-setup: POST /config/network want 303, got %d body=%s", recCfg.Code, recCfg.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path) // must now pass the full Validate
	if err != nil {
		t.Fatalf("partial-setup: config must be fully board-valid after finishing: %v", err)
	}
	if cur.Board.Origin != "PAD" || cur.Darwin.Token != "tok-finish" {
		t.Fatalf("partial-setup: finish didn't persist origin/token: %+v", cur)
	}
	if cur.Wifi.SSID != "HomeNet" || cur.Wifi.PSK != "supersecret" {
		t.Fatalf("partial-setup: finish must keep the portal-supplied wifi creds: %+v", cur.Wifi)
	}
}
