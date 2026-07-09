package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

// statusTestPassword is the admin password newTestServerWithSources sets up,
// ready for loginAs.
const statusTestPassword = "longenough1"

// newTestServerWithSources wires a Server over a valid, saved config (admin
// password already set) whose Sources are exactly src, for tests that need
// to control Snapshot/Ring rather than the newTestService defaults (which
// are all nil/empty).
func newTestServerWithSources(t *testing.T, src Sources) (*Server, *Service) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, validCfg()); err != nil {
		t.Fatal(err)
	}
	svc := NewService(path, src, Actions{Apply: func() {}, Reboot: func() error { return nil }}, testLog())
	if err := svc.SetInitialPassword(statusTestPassword, "PAD", ""); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	return NewServer(svc, testLog()), svc
}

// statusTestSources builds Sources with a fixed StateDepartures snapshot and
// a ring pre-loaded with 3 events (oldest/middle/newest, added in that
// order).
func statusTestSources(t *testing.T) Sources {
	t.Helper()
	fetchedAt := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	snap := &board.Snapshot{State: board.StateDepartures, FetchedAt: fetchedAt}

	ring := obs.NewRing(8)
	ring.Add(obs.Event{Time: fetchedAt, Level: slog.LevelInfo, Msg: "oldest-event-msg"})
	ring.Add(obs.Event{Time: fetchedAt.Add(time.Minute), Level: slog.LevelInfo, Msg: "middle-event-msg"})
	ring.Add(obs.Event{Time: fetchedAt.Add(2 * time.Minute), Level: slog.LevelInfo, Msg: "newest-event-msg"})

	return Sources{
		Snapshot:  func() *board.Snapshot { return snap },
		Ring:      ring,
		Version:   "v-status-test",
		StartedAt: time.Now().Add(-time.Hour),
	}
}

// (a) unauthenticated GET / redirects to /login.
func TestStatusPageUnauthenticatedRedirects(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	rec := getPath(t, srv.Handler(), "/")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// (a2) authed GET to an unknown path (e.g. /nonexistent, /favicon.ico) must
// 404, not fall through to the status page: GET / is a catch-all
// registration in net/http's mux, so without an explicit path guard
// handleIndex would render the status page as 200 for any authed request to
// any unregistered path.
func TestStatusPageAuthedUnknownPathIs404(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)

	rec := getPath(t, srv.Handler(), "/nonexistent", cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = getPath(t, srv.Handler(), "/", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ must still be 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// (b) authed GET / shows the departures state, the version string, and
// lists the newest event before the oldest (newest-first ordering visible
// in the rendered HTML).
func TestStatusPageAuthedShowsDeparturesVersionAndEventOrder(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)

	rec := getPath(t, srv.Handler(), "/", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "departures") {
		t.Fatalf("expected state 'departures' in body: %s", body)
	}
	if !strings.Contains(body, "v-status-test") {
		t.Fatalf("expected version string in body: %s", body)
	}
	newestIdx := strings.Index(body, "newest-event-msg")
	oldestIdx := strings.Index(body, "oldest-event-msg")
	if newestIdx == -1 || oldestIdx == -1 {
		t.Fatalf("expected both events present: %s", body)
	}
	if newestIdx >= oldestIdx {
		t.Fatalf("expected newest event before oldest (newest-first): newest@%d oldest@%d body=%s", newestIdx, oldestIdx, body)
	}
}

// (c) authed GET /events returns only the event rows (htmx partial), not a
// full HTML document.
func TestStatusPageEventsPartialOnlyEventRows(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)

	rec := getPath(t, srv.Handler(), "/events", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") {
		t.Fatalf("events partial must not include a full page: %s", body)
	}
	if !strings.Contains(body, "newest-event-msg") || !strings.Contains(body, "oldest-event-msg") {
		t.Fatalf("expected event rows in partial: %s", body)
	}
}

// (d) Sources.MDNSState left nil (the zero value statusTestSources uses) is
// nil-tolerated: Service.MDNSState() reads as "" (feature off), matching
// SoakRemaining's nil-tolerance pattern.
func TestServiceMDNSStateNilSourceSafe(t *testing.T) {
	_, svc := newTestServerWithSources(t, statusTestSources(t))
	if got := svc.MDNSState(); got != "" {
		t.Fatalf("MDNSState() with nil Sources.MDNSState = %q, want empty", got)
	}
}

// (e) the status page renders the mDNS dd only when Sources.MDNSState
// reports a non-empty hostname.
func TestStatusPageShowsMDNSNameOnlyWhenSet(t *testing.T) {
	off := statusTestSources(t)
	srvOff, _ := newTestServerWithSources(t, off)
	cookieOff, _ := loginAs(t, srvOff, statusTestPassword)

	rec := getPath(t, srvOff.Handler(), "/", cookieOff)
	if strings.Contains(rec.Body.String(), "trainboard-ab12.local") {
		t.Fatalf("mDNS row must not render without Sources.MDNSState: %s", rec.Body.String())
	}

	on := statusTestSources(t)
	on.MDNSState = func() string { return "trainboard-ab12.local" }
	srvOn, _ := newTestServerWithSources(t, on)
	cookieOn, _ := loginAs(t, srvOn, statusTestPassword)

	rec = getPath(t, srvOn.Handler(), "/", cookieOn)
	if !strings.Contains(rec.Body.String(), "trainboard-ab12.local") {
		t.Fatalf("expected mDNS hostname in body: %s", rec.Body.String())
	}
}

// (f) GET /preview.png is gone entirely: the live board (board.js) now
// renders client-side from GET /api/board, so the old PNG route must 404
// like any other unregistered path (handleIndex's catch-all guard).
func TestPreviewPNGGone(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)

	req := httptest.NewRequest(http.MethodGet, "/preview.png", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("preview.png must be gone: want 404, got %d", rec.Code)
	}
}

// (g) the status page's board container carries the markup contract
// board.js depends on: the #board element, its data-endpoint, and the
// script tag that loads it.
func TestStatusPageHasBoardContainer(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)

	rec := getPath(t, srv.Handler(), "/", cookie)
	body := rec.Body.String()
	for _, want := range []string{`id="board"`, `data-endpoint="/api/board"`, `/static/board.js`} {
		if !strings.Contains(body, want) {
			t.Errorf("status page missing %q", want)
		}
	}
}
