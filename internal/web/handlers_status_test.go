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

// statusTestSources builds Sources with a StateDepartures snapshot fetched
// "just now" (so the status page deterministically renders the fresh
// "Running normally" branch, never the staleAfter one) and a ring pre-loaded
// with 3 events (oldest/middle/newest, added in that order).
func statusTestSources(t *testing.T) Sources {
	t.Helper()
	fetchedAt := time.Now()
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
	// The fixture's LastFetch is fresh (time.Now()), so the full render
	// path must deterministically take stateLine's non-stale departures
	// branch: the exact "Running normally" label with an unmodified
	// (green) dot.
	if !strings.Contains(body, "Running normally") {
		t.Fatalf("expected 'Running normally' state label in body: %s", body)
	}
	if !strings.Contains(body, `<span class="dot"></span>`) {
		t.Fatalf("expected unmodified green dot (no amber/red class) in body: %s", body)
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
	// Severity must not be color-only (WCAG 1.4.1): every row carries a
	// visually-hidden text label alongside the pip.
	if !strings.Contains(body, `<span class="vh">INFO</span>`) {
		t.Fatalf("expected visually-hidden level text next to the pip: %s", body)
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

// (e) the status page's Address row appends the mDNS alias hostname only
// when Sources.MDNSState reports a non-empty hostname (the row shows the
// fixed "trainboard.local" alias, not the per-device unique name — see
// internal/mdns/records.go's aliasName).
func TestStatusPageShowsMDNSNameOnlyWhenSet(t *testing.T) {
	off := statusTestSources(t)
	srvOff, _ := newTestServerWithSources(t, off)
	cookieOff, _ := loginAs(t, srvOff, statusTestPassword)

	rec := getPath(t, srvOff.Handler(), "/", cookieOff)
	if strings.Contains(rec.Body.String(), "trainboard.local") {
		t.Fatalf("mDNS alias must not render without Sources.MDNSState: %s", rec.Body.String())
	}

	on := statusTestSources(t)
	on.MDNSState = func() string { return "trainboard-ab12.local" }
	srvOn, _ := newTestServerWithSources(t, on)
	cookieOn, _ := loginAs(t, srvOn, statusTestPassword)

	rec = getPath(t, srvOn.Handler(), "/", cookieOn)
	if !strings.Contains(rec.Body.String(), "trainboard.local") {
		t.Fatalf("expected mDNS alias hostname in body: %s", rec.Body.String())
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

// The board preview is a fixed 256×64 stage scaled to its wrapper (#61).
// The wrapper keeps the role/aria of the old .board element.
func TestStatusBoardStageMarkup(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)
	body := getPath(t, srv.Handler(), "/", cookie).Body.String()
	for _, want := range []string{
		`class="boardwrap" id="board"`,
		`data-endpoint="/api/board"`,
		`role="img"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("status page missing %q", want)
		}
	}
	if strings.Contains(body, `class="board"`) {
		t.Errorf("old .board container still present")
	}
}

// TestStateLine locks down the runtime-state-to-headline mapping the status
// page's statebar reads: label text, css class ("ok"|"warn"|"bad"), for the
// states an operator actually sees plus the stale-fetch override.
func TestStateLine(t *testing.T) {
	cases := []struct {
		name      string
		st        StatusData
		wantLabel string
		wantClass string
	}{
		{"departures", StatusData{State: "departures", LastFetch: time.Now()}, "Running normally", "ok"},
		{"no services", StatusData{State: "no-services", LastFetch: time.Now()}, "Running — no services to show", "ok"},
		{"initialising", StatusData{State: "initialising"}, "Starting up", "warn"},
		{"clock", StatusData{State: "clock-not-synced"}, "Waiting for clock sync", "warn"},
		{"fault", StatusData{State: "error", Fault: "E02"}, "Fault E02", "bad"},
		{"stale", StatusData{State: "departures", LastFetch: time.Now().Add(-10 * time.Minute)}, "Running — data is stale", "warn"},
	}
	for _, c := range cases {
		label, class, _ := stateLine(c.st, time.Now())
		if label != c.wantLabel || class != c.wantClass {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", c.name, label, class, c.wantLabel, c.wantClass)
		}
	}
}
