package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/config"
)

// putJSON issues a PUT with a JSON body and (if non-empty) the API's
// X-CSRF-Token header, through the full Handler() middleware stack.
func putJSON(t *testing.T, h http.Handler, path string, body []byte, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// postJSON issues a POST with no body (the API action routes take none) and
// the API's X-CSRF-Token header, through the full Handler() middleware stack.
func postJSON(t *testing.T, h http.Handler, path string, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeAPIError(t *testing.T, rec *httptest.ResponseRecorder) apiError {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json (body=%s)", ct, rec.Body.String())
	}
	var e apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode apiError: %v (body=%s)", err, rec.Body.String())
	}
	if e.Error == "" {
		t.Fatalf("expected non-empty error message, body=%s", rec.Body.String())
	}
	return e
}

// (a) GET /api/status unauthenticated -> 401 JSON {"error":"unauthorized"}.
func TestAPIStatusUnauthenticated401JSON(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	rec := getPath(t, srv.Handler(), "/api/status")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	e := decodeAPIError(t, rec)
	if e.Error != "unauthorized" {
		t.Fatalf("error = %q, want %q", e.Error, "unauthorized")
	}
}

// (b) GET /api/status authed -> 200 with a "state" field and an RFC3339
// lastFetch.
func TestAPIStatusAuthedHasStateAndRFC3339LastFetch(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)

	rec := getPath(t, srv.Handler(), "/api/status", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got statusJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode statusJSON: %v (body=%s)", err, rec.Body.String())
	}
	if got.State != "departures" {
		t.Fatalf("state = %q, want %q", got.State, "departures")
	}
	if _, err := time.Parse(time.RFC3339, got.LastFetch); err != nil {
		t.Fatalf("lastFetch %q did not parse as RFC3339: %v", got.LastFetch, err)
	}
	if len(got.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(got.Events))
	}
	if got.Events[0].Msg != "newest-event-msg" {
		t.Fatalf("events[0].msg = %q, want newest-first", got.Events[0].Msg)
	}
}

// (c) GET /api/config -> the token field is the redaction marker, never the
// real stored token.
func TestAPIConfigGetRedactsToken(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/api/config", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode config.Config: %v (body=%s)", err, rec.Body.String())
	}
	if got.Darwin.Token == configTestToken {
		t.Fatalf("real Darwin token leaked into /api/config response: %s", rec.Body.String())
	}
	if got.Darwin.Token == "" {
		t.Fatalf("expected a redaction marker, got empty token")
	}
}

// (d) unauthenticated GET /api/config -> 401 JSON.
func TestAPIConfigGetUnauthenticated401JSON(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	rec := getPath(t, srv.Handler(), "/api/config")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	decodeAPIError(t, rec)
}

// (e) PUT /api/config with the CSRF header and a valid body -> 200
// {"status":"applied"}, the file is changed, and Actions.Apply fires.
func TestAPIConfigPutValidAppliesAndPersists(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Board.RefreshSeconds = 90
	body, err := json.Marshal(configUpdateJSON{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}

	rec := putJSON(t, srv.Handler(), "/api/config", body, csrf, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "applied" {
		t.Fatalf("status = %q, want %q", got["status"], "applied")
	}

	cur, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cur.Board.RefreshSeconds != 90 {
		t.Fatalf("board.refreshSeconds = %d, want 90", cur.Board.RefreshSeconds)
	}
	awaitApply(t, applyCh)
}

// (f) PUT /api/config with a bad CSRF token -> 403 JSON, and the file is
// unchanged.
func TestAPIConfigPutBadCSRFRejected(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	cfg := before
	cfg.Board.RefreshSeconds = 90
	body, err := json.Marshal(configUpdateJSON{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}

	rec := putJSON(t, srv.Handler(), "/api/config", body, "wrong-csrf-token", cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	decodeAPIError(t, rec)

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Board.RefreshSeconds != before.Board.RefreshSeconds {
		t.Fatalf("config file must be unchanged on CSRF rejection: before=%+v after=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// (g) PUT /api/config with a validation failure (refreshSeconds below the
// minimum) -> 400 JSON error, and the file is unchanged.
func TestAPIConfigPutValidationFailure400JSON(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	cfg := before
	cfg.Board.RefreshSeconds = 5
	body, err := json.Marshal(configUpdateJSON{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}

	rec := putJSON(t, srv.Handler(), "/api/config", body, csrf, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	e := decodeAPIError(t, rec)
	if e.Error == "" {
		t.Fatal("expected a validation error message")
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Board.RefreshSeconds != before.Board.RefreshSeconds {
		t.Fatalf("config file must be unchanged on validation failure: before=%+v after=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// (h) PUT /api/config with an invalid JSON body -> 400 JSON error.
func TestAPIConfigPutMalformedBody400JSON(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := putJSON(t, srv.Handler(), "/api/config", []byte("{not json"), csrf, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	decodeAPIError(t, rec)
}

// (i) GET /api/events returns events newest-first.
func TestAPIEventsNewestFirst(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)

	rec := getPath(t, srv.Handler(), "/api/events", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var events []eventJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v (body=%s)", err, rec.Body.String())
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[0].Msg != "newest-event-msg" || events[2].Msg != "oldest-event-msg" {
		t.Fatalf("events not newest-first: %+v", events)
	}
}

// (j) unauthenticated GET /api/events -> 401 JSON.
func TestAPIEventsUnauthenticated401JSON(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	rec := getPath(t, srv.Handler(), "/api/events")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	decodeAPIError(t, rec)
}

// (k) POST /api/actions/restart authed+csrf fires Actions.Apply and replies
// {"status":"applied"}.
func TestAPIActionsRestartFiresApply(t *testing.T) {
	srv, _, _, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := postJSON(t, srv.Handler(), "/api/actions/restart", csrf, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "applied" {
		t.Fatalf("status = %q, want %q", got["status"], "applied")
	}
	awaitApply(t, applyCh)
}

// (l) POST /api/actions/reboot authed+csrf fires Actions.Reboot and replies
// {"status":"rebooting"}; a Reboot error instead replies 500 JSON.
func TestAPIActionsRebootFiresRebootAndReportsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, validCfg()); err != nil {
		t.Fatal(err)
	}
	svc := newTestServiceAt(t, path)
	rebootCh := make(chan struct{}, 1)
	var rebootErr error
	svc.act = Actions{
		Apply: func() {},
		Reboot: func() error {
			rebootCh <- struct{}{}
			return rebootErr
		},
	}
	if err := svc.SetInitialPassword("longenough1", "PAD", ""); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	srv := NewServer(svc, testLog())
	cookie, csrf := loginAs(t, srv, "longenough1")

	rec := postJSON(t, srv.Handler(), "/api/actions/reboot", csrf, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "rebooting" {
		t.Fatalf("status = %q, want %q", got["status"], "rebooting")
	}
	select {
	case <-rebootCh:
	default:
		t.Fatal("Actions.Reboot was not called")
	}

	rebootErr = errors.New("boom: reboot command failed")
	rec2 := postJSON(t, srv.Handler(), "/api/actions/reboot", csrf, cookie)
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	decodeAPIError(t, rec2)
}

// (l2) POST /api/actions/restart without X-CSRF-Token -> 403 JSON.
func TestAPIActionsRestartMissingCSRFRejected403JSON(t *testing.T) {
	srv, _, _, applyCh := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	rec := postJSON(t, srv.Handler(), "/api/actions/restart", "", cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	decodeAPIError(t, rec)
	assertApplyNotCalled(t, applyCh)
}

// (l3) POST /api/actions/reboot without X-CSRF-Token -> 403 JSON.
func TestAPIActionsRebootMissingCSRFRejected403JSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, validCfg()); err != nil {
		t.Fatal(err)
	}
	svc := newTestServiceAt(t, path)
	rebootCh := make(chan struct{}, 1)
	svc.act = Actions{
		Apply: func() {},
		Reboot: func() error {
			rebootCh <- struct{}{}
			return nil
		},
	}
	if err := svc.SetInitialPassword("longenough1", "PAD", ""); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	srv := NewServer(svc, testLog())
	cookie, _ := loginAs(t, srv, "longenough1")

	rec := postJSON(t, srv.Handler(), "/api/actions/reboot", "", cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	decodeAPIError(t, rec)
	select {
	case <-rebootCh:
		t.Fatal("Actions.Reboot must not be called when CSRF is missing")
	default:
	}
}

// (m) 31 rapid state-changing API requests trip the shared rate limiter
// (burst 30) on the 31st, surfaced as JSON rather than the middleware's
// default plain text.
func TestAPIRateLimitedRespondsJSON(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)

	var lastCode int
	var lastRec *httptest.ResponseRecorder
	for i := 0; i < 31; i++ {
		rec := putJSON(t, srv.Handler(), "/api/config", []byte(`{}`), "")
		lastCode = rec.Code
		lastRec = rec
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("want 429 on the 31st rapid request, got %d", lastCode)
	}
	decodeAPIError(t, lastRec)
	// apiJSONErrors rewrote the rate limiter's plain-text 403 body into JSON
	// here, so it must also set nosniff on the rewritten response (N7).
	if xcto := lastRec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", xcto)
	}
}
