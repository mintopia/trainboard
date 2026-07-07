package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
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
// to control Snapshot/Ring/PreviewPNG rather than the newTestService
// defaults (which are all nil/empty).
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

// tinyPNG encodes a 1x1 image, a minimal-but-valid PNG payload.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.Gray{Y: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// statusTestSources builds Sources with: a fixed StateDepartures snapshot, a
// ring pre-loaded with 3 events (oldest/middle/newest, added in that order),
// and a PreviewPNG returning a valid tiny PNG.
func statusTestSources(t *testing.T) Sources {
	t.Helper()
	fetchedAt := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	snap := &board.Snapshot{State: board.StateDepartures, FetchedAt: fetchedAt}

	ring := obs.NewRing(8)
	ring.Add(obs.Event{Time: fetchedAt, Level: slog.LevelInfo, Msg: "oldest-event-msg"})
	ring.Add(obs.Event{Time: fetchedAt.Add(time.Minute), Level: slog.LevelInfo, Msg: "middle-event-msg"})
	ring.Add(obs.Event{Time: fetchedAt.Add(2 * time.Minute), Level: slog.LevelInfo, Msg: "newest-event-msg"})

	pngBytes := tinyPNG(t)
	return Sources{
		Snapshot:   func() *board.Snapshot { return snap },
		Ring:       ring,
		PreviewPNG: func() []byte { return pngBytes },
		Version:    "v-status-test",
		StartedAt:  time.Now().Add(-time.Hour),
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

// (c) authed GET /preview.png serves the PNG with the right content type and
// cache headers, and the body decodes as a valid PNG.
func TestStatusPagePreviewPNGServesImage(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	cookie, _ := loginAs(t, srv, statusTestPassword)

	rec := getPath(t, srv.Handler(), "/preview.png", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	if _, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("body did not decode as PNG: %v", err)
	}
}

// (d) a PreviewPNG returning nil is a 404, not an empty 200.
func TestStatusPagePreviewPNGNilReturns404(t *testing.T) {
	src := statusTestSources(t)
	src.PreviewPNG = func() []byte { return nil }
	srv, _ := newTestServerWithSources(t, src)
	cookie, _ := loginAs(t, srv, statusTestPassword)

	rec := getPath(t, srv.Handler(), "/preview.png", cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// (e) unauthenticated GET /preview.png redirects to /login.
func TestStatusPagePreviewPNGUnauthenticatedRedirects(t *testing.T) {
	srv, _ := newTestServerWithSources(t, statusTestSources(t))
	rec := getPath(t, srv.Handler(), "/preview.png")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// (f) authed GET /events returns only the event rows (htmx partial), not a
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
