package web

import (
	"net/http"
	"time"
)

// handleActionsGet renders the actions page: restart, reboot, and the
// (currently disabled) firmware-update button.
func (s *Server) handleActionsGet(w http.ResponseWriter, r *http.Request) {
	s.render(w, "actions", basePage{LoggedIn: true, CSRF: csrfFrom(r)})
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
