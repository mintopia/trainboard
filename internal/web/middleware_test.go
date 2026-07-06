package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mintopia/trainboard/internal/obs"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func authedRequest(t *testing.T, s *Sessions, method, target string, body io.Reader) (*http.Request, string) {
	t.Helper()
	tok, csrf := s.Create()
	r := httptest.NewRequest(method, target, body)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	return r, csrf
}

func TestRequireAuthRedirectsHTML(t *testing.T) {
	s := NewSessions()
	h := chain(okHandler(), requireAuth(s, false))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRequireAuth401API(t *testing.T) {
	s := NewSessions()
	h := chain(okHandler(), requireAuth(s, true))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestRequireAuthPassesValidSession(t *testing.T) {
	s := NewSessions()
	h := chain(okHandler(), requireAuth(s, false))
	r, _ := authedRequest(t, s, "GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestCSRFRejectsMissingAndWrongToken(t *testing.T) {
	s := NewSessions()
	h := chain(okHandler(), requireAuth(s, false), csrfProtect(testLog()))
	for _, tok := range []string{"", "wrong"} {
		form := url.Values{"csrf": {tok}}
		r, _ := authedRequest(t, s, "POST", "/config", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("csrf %q: want 403, got %d", tok, rec.Code)
		}
	}
}

func TestCSRFAcceptsFormFieldAndHeader(t *testing.T) {
	s := NewSessions()
	h := chain(okHandler(), requireAuth(s, false), csrfProtect(testLog()))
	// form field
	r, csrf := authedRequest(t, s, "POST", "/config", nil)
	form := url.Values{"csrf": {csrf}}
	r2 := httptest.NewRequest("POST", "/config", strings.NewReader(form.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2.AddCookie(r.Cookies()[0])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r2)
	if rec.Code != http.StatusOK {
		t.Fatalf("form-field csrf: want 200, got %d", rec.Code)
	}
	// header
	r3, csrf3 := authedRequest(t, s, "POST", "/api/config", nil)
	r3.Header.Set("X-CSRF-Token", csrf3)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r3)
	if rec.Code != http.StatusOK {
		t.Fatalf("header csrf: want 200, got %d", rec.Code)
	}
}

func TestCSRFIgnoresGET(t *testing.T) {
	s := NewSessions()
	h := chain(okHandler(), requireAuth(s, false), csrfProtect(testLog()))
	r, _ := authedRequest(t, s, "GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET must skip csrf, got %d", rec.Code)
	}
}

func TestCSRFRejectionLogsToObs(t *testing.T) {
	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(io.Discard, ring, slog.LevelWarn)
	s := NewSessions()
	h := chain(okHandler(), requireAuth(s, false), csrfProtect(log))
	form := url.Values{"csrf": {"wrong"}}
	r, _ := authedRequest(t, s, "POST", "/config", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	var found bool
	for _, e := range ring.Events() {
		if e.Msg == "csrf rejected" {
			found = true
			if e.Attrs["path"] != "/config" || e.Attrs["method"] != "POST" {
				t.Fatalf("unexpected attrs: %+v", e.Attrs)
			}
			for _, v := range e.Attrs {
				if v == "wrong" {
					t.Fatalf("csrf token value leaked into log attrs: %+v", e.Attrs)
				}
			}
		}
	}
	if !found {
		t.Fatal("expected a \"csrf rejected\" event in the obs ring")
	}
}

func TestRecoverPanicsMiddleware(t *testing.T) {
	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(io.Discard, ring, slog.LevelWarn)
	panicky := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})
	h := chain(panicky, recoverPanics(log))
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if v := recover(); v != nil {
				t.Fatalf("recoverPanics did not stop the panic from propagating: %v", v)
			}
		}()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	}()

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	var found bool
	for _, e := range ring.Events() {
		if e.Msg == "handler panic" {
			found = true
			if e.Attrs["path"] != "/" {
				t.Fatalf("unexpected attrs: %+v", e.Attrs)
			}
			if !strings.Contains(e.Attrs["panic"], "boom") {
				t.Fatalf("panic value missing from log attrs: %+v", e.Attrs)
			}
		}
	}
	if !found {
		t.Fatal("expected a \"handler panic\" event in the obs ring")
	}
}

func TestOriginCheck(t *testing.T) {
	h := chain(okHandler(), originCheck(testLog()))
	cases := []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},                           // non-browser client
		{"http://trainboard.local", http.StatusOK},    // same host
		{"http://evil.example", http.StatusForbidden}, // cross-origin
		{"null", http.StatusForbidden},                // sandboxed/nulled
	}
	for _, tc := range cases {
		r := httptest.NewRequest("POST", "http://trainboard.local/config", nil)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Errorf("origin %q: want %d, got %d", tc.origin, tc.want, rec.Code)
		}
	}
	// GET with a foreign Origin is fine (reads are not state-changing).
	r := httptest.NewRequest("GET", "http://trainboard.local/", nil)
	r.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Errorf("GET cross-origin: want 200, got %d", rec.Code)
	}
}

func TestRateLimitMiddleware429(t *testing.T) {
	rl := newLimiter(2)
	h := chain(okHandler(), rateLimit(rl, testLog()))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/login", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/login", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}
	// GETs are never limited.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET must bypass rate limit, got %d", rec.Code)
	}
}

func TestLogRequestsOmitsQueryString(t *testing.T) {
	var sb strings.Builder
	log := slog.New(slog.NewTextHandler(&sb, nil))
	h := chain(okHandler(), logRequests(log))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/config?token=SECRET", nil))
	if strings.Contains(sb.String(), "SECRET") {
		t.Fatalf("query string leaked into log: %s", sb.String())
	}
	if !strings.Contains(sb.String(), "/config") {
		t.Fatalf("path missing from log: %s", sb.String())
	}
}
