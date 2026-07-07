package web

import (
	"fmt"
	"net/http"
	"time"
)

// statusPageData is the render data for the authed status page: basePage
// (nav/CSRF) plus the live StatusData and a humanised uptime string (
// time.Duration has no template-friendly format verb, so render computes it
// once here rather than in the template).
type statusPageData struct {
	basePage
	Status     StatusData
	UptimeText string
}

// handleIndex renders the authed status page: board state/fault, version,
// uptime, last fetch time, local addresses, and the recent-events feed.
//
// It is registered on GET / — net/http's catch-all pattern — so without the
// path guard below, any authed request to an unregistered path (e.g.
// /favicon.ico, /nonexistent) would silently render the status page as a
// 200 instead of a 404.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	st := s.svc.Status()
	s.render(w, "index", statusPageData{
		basePage:   basePage{LoggedIn: true, CSRF: csrfFrom(r)},
		Status:     st,
		UptimeText: humanUptime(st.Uptime),
	})
}

// humanUptime renders a duration the way an operator wants to read it on the
// status page: "3h12m" once an hour has passed (seconds no longer matter at
// that scale), "12m34s" under an hour, "34s" under a minute.
func humanUptime(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	sec := d / time.Second

	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, sec)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

// handlePreviewPNG streams the current live panel preview with headers that
// keep it fresh on every request: no caching, no CDN, always the latest
// frame. Sources.PreviewPNG returning nil or empty (preview not yet
// available, e.g. before the board's first render) is a 404, not an empty
// 200 — the status page's polling <img> and any test decoding the body both
// need to be able to tell "no preview yet" apart from "a valid empty image".
func (s *Server) handlePreviewPNG(w http.ResponseWriter, r *http.Request) {
	data := s.svc.src.PreviewPNG()
	if len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// handleEvents renders just the "eventlist" partial for htmx's polling
// GET /events, so the status page's event feed can refresh without a full
// page reload.
func (s *Server) handleEvents(w http.ResponseWriter, _ *http.Request) {
	st := s.svc.Status()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := statusTemplate.ExecuteTemplate(w, "eventlist", st.Events); err != nil {
		s.log.Error("template render failed", "page", "events", "error", err.Error())
	}
}
