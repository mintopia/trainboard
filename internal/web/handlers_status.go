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
	// SoakRemainingText is the humanised running-soak countdown; "" hides
	// the row.
	SoakRemainingText string
	// MDNSState is the board's mDNS hostname (e.g. "trainboard-ab12.local");
	// "" hides the row (feature off or --mdns=false).
	MDNSState string
	// StateLabel, StateClass ("ok"|"warn"|"bad") and StateDetail are the
	// statebar's headline, computed once by stateLine so the template does
	// no state logic of its own.
	StateLabel  string
	StateClass  string
	StateDetail string
	// HotspotActive mirrors Service.Hotspot() != nil: true while the board
	// is running its own AP because it couldn't join the configured WiFi.
	HotspotActive bool
	// CheckedNow is true when this render immediately follows an explicit
	// "Check for updates" (the /?checked=1 PRG landing): the template may
	// affirm "up to date" rather than silently showing no banner.
	CheckedNow bool
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
	data := statusPageData{
		basePage:      basePage{LoggedIn: true, CSRF: csrfFrom(r), Active: "status"},
		Status:        st,
		UptimeText:    humanUptime(st.Uptime),
		MDNSState:     s.svc.MDNSState(),
		HotspotActive: s.svc.Hotspot() != nil,
		CheckedNow:    r.URL.Query().Get("checked") == "1",
	}
	data.StateLabel, data.StateClass, data.StateDetail = stateLine(st, time.Now())
	if st.SoakRemaining > 0 {
		data.SoakRemainingText = humanUptime(st.SoakRemaining)
	}
	s.render(w, "index", data)
}

// staleAfter is how old the last successful fetch may be before the status
// page calls the data stale (2× the max refresh anyone sane configures).
const staleAfter = 5 * time.Minute

// stateLine maps runtime state to the status page's headline: label, css
// class ("ok"|"warn"|"bad"), and a short detail sentence (may be empty).
func stateLine(st StatusData, now time.Time) (label, class, detail string) {
	switch st.State {
	case "departures", "no-services":
		if !st.LastFetch.IsZero() && now.Sub(st.LastFetch) > staleAfter {
			return "Running — data is stale", "warn",
				"Last successful fetch " + st.LastFetch.Format("15:04:05") + ". Check recent events below."
		}
		if st.State == "no-services" {
			return "Running — no services to show", "ok", ""
		}
		return "Running normally", "ok", ""
	case "initialising":
		return "Starting up", "warn", "The board is connecting and fetching first departures."
	case "clock-not-synced":
		return "Waiting for clock sync", "warn", "Departure times need an accurate clock; this resolves itself within a minute or two of network access."
	case "error":
		f := st.Fault
		if f == "" {
			f = "unknown"
		}
		return "Fault " + f, "bad", "The panel shows details. Recent events below usually name the cause."
	default:
		return st.State, "warn", ""
	}
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
