package web

import (
	"net/http"
	"time"
)

// actionsPageData renders the actions page: base nav/CSRF plus the burn-in
// soak block's state.
type actionsPageData struct {
	basePage
	// SoakRemaining is the humanised remaining time ("3h12m"); "" = no soak
	// running (renders the start form instead of the cancel form).
	SoakRemaining string
	// SoakError re-renders the start form with a validation message.
	SoakError string
	// HotspotActive reports whether the board is currently in AP mode
	// (Service.Hotspot() != nil); the page shows the wifi-retry-now form
	// only while this is true.
	HotspotActive bool
}

// handleActionsGet renders the actions page: restart, reboot, the burn-in
// soak block, and the (currently disabled) firmware-update button.
func (s *Server) handleActionsGet(w http.ResponseWriter, r *http.Request) {
	s.render(w, "actions", s.actionsData(r, ""))
}

// actionsData assembles actionsPageData with the live soak state.
func (s *Server) actionsData(r *http.Request, soakError string) actionsPageData {
	d := actionsPageData{basePage: basePage{LoggedIn: true, CSRF: csrfFrom(r)}, SoakError: soakError}
	if rem := s.svc.SoakRemaining(); rem > 0 {
		d.SoakRemaining = humanUptime(rem)
	}
	d.HotspotActive = s.svc.Hotspot() != nil
	return d
}

// scheduleApply fires Actions.Apply after applyDelay, once the caller's
// response has been written — the shared timing mechanism behind every
// route that ends in an Apply-triggered restart: config save
// (handleConfigPost), the actions page's restart button, and the JSON API's
// PUT /api/config and POST /api/actions/restart.
func (s *Server) scheduleApply() {
	time.AfterFunc(applyDelay, s.svc.act.Apply)
}

// handleActionsRestart renders the same applied page config save uses (its
// "saved, restarting" copy is accurate here too — a software restart, not a
// config change) and schedules Actions.Apply, exactly like handleConfigPost.
func (s *Server) handleActionsRestart(w http.ResponseWriter, r *http.Request) {
	s.render(w, "applied", basePage{LoggedIn: true, CSRF: csrfFrom(r)})
	s.scheduleApply()
}

// handleActionsReboot calls Actions.Reboot synchronously, unlike restart's
// Apply: Reboot can fail (e.g. the underlying system call could not be
// issued) and, unlike Apply, returns an error to say so. That error can only
// be surfaced as an HTTP 500 if it is known before the response is written,
// so — deliberately, unlike restart — there is no render-then-delay here:
// the call happens first, and the page reflects its actual outcome.
func (s *Server) handleActionsReboot(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.act.Reboot(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "rebooting", basePage{LoggedIn: true, CSRF: csrfFrom(r)})
}

// handleActionsSoak starts a burn-in soak with the form's duration
// (1h/4h/8h, validated by Service.StartSoak) and redirects back to the
// actions page, which now shows the countdown + cancel form (PRG — unlike
// restart/reboot there is no terminal "applied" page here; the natural next
// view is the actions page's running-soak state).
func (s *Server) handleActionsSoak(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.svc.StartSoak(r.PostFormValue("duration")); err != nil {
		s.render(w, "actions", s.actionsData(r, err.Error()))
		return
	}
	http.Redirect(w, r, "/actions", http.StatusFound)
}

// handleActionsSoakCancel ends any running soak (idle cancel is a no-op)
// and redirects back to the actions page.
func (s *Server) handleActionsSoakCancel(w http.ResponseWriter, r *http.Request) {
	s.svc.CancelSoak()
	http.Redirect(w, r, "/actions", http.StatusFound)
}

// handleActionsWifiRetry asks the connectivity manager to attempt the
// configured WiFi immediately (Service.WifiRetryNow; tears the AP down for
// ~20s) and redirects back to the actions page — PRG, like the soak
// start/cancel handlers, since there is no terminal "applied" page here.
func (s *Server) handleActionsWifiRetry(w http.ResponseWriter, r *http.Request) {
	s.svc.WifiRetryNow()
	http.Redirect(w, r, "/actions", http.StatusFound)
}
