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
	d := actionsPageData{basePage: basePage{LoggedIn: true, CSRF: csrfFrom(r), Active: "actions"}, SoakError: soakError}
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

// scheduleWifiRetry fires Service.WifiRetryNow after applyDelay, once the
// caller's response has been written — the same render-then-delay shape as
// scheduleApply, used by the AP-mode partial setup's credential-handoff
// success path (handleSetupPostAPMode) so the phone receives the "hotspot is
// about to drop" page before the AP actually tears down.
func (s *Server) scheduleWifiRetry() {
	time.AfterFunc(applyDelay, s.svc.WifiRetryNow)
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

// handleUpdateCheck runs an on-demand release check and returns to the
// status page (PRG); the outcome lands in Status.Update.LastError /
// .Available, which the page renders.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.CheckForUpdate(r.Context()); err != nil {
		s.log.Warn("update check failed", "error", err.Error())
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleUpdateApply stages the available update, then — success only —
// renders the applied page and schedules the same clean-exit restart as
// config save (the launcher boots the new slot). Failure redirects to the
// status page, whose Software section shows Status.LastError; the current
// binary keeps running (update failures are non-fatal by design).
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.ApplyUpdate(r.Context()); err != nil {
		s.log.Error("update apply failed", "error", err.Error())
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.render(w, "applied", basePage{LoggedIn: true, CSRF: csrfFrom(r)})
	s.scheduleApply()
}

// handleUpdateDismiss clears the rollback banner (PRG).
func (s *Server) handleUpdateDismiss(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DismissRollback(); err != nil {
		s.log.Warn("rollback dismiss failed", "error", err.Error())
	}
	http.Redirect(w, r, "/", http.StatusFound)
}
