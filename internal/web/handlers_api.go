package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/stations"
	"github.com/mintopia/trainboard/internal/update"
)

// apiError is the uniform JSON error shape for every /api/* failure:
// {"error":"..."}. Status codes follow ordinary HTTP semantics (400
// validation, 401 unauthorized, 403 forbidden, 429 too many requests, 500
// internal).
type apiError struct {
	Error string `json:"error"`
}

// eventJSON is obs.Event's lowerCamel JSON projection. obs.Event itself
// carries no json tags (it is not an API type), so the mapping happens here.
type eventJSON struct {
	Time  string            `json:"time"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

func toEventJSON(e obs.Event) eventJSON {
	return eventJSON{
		Time:  e.Time.Format(rfc3339),
		Level: e.Level.String(),
		Msg:   e.Msg,
		Attrs: e.Attrs,
	}
}

// rfc3339 is used to format every timestamp this API emits, so lastFetch and
// event times are always RFC3339 regardless of any fractional-second
// trimming time.Time's default JSON marshalling would otherwise apply.
const rfc3339 = "2006-01-02T15:04:05Z07:00"

// statusJSON is StatusData's lowerCamel JSON projection. StatusData itself
// carries no json tags (it is the HTML template's data type, not an API
// type), so the mapping happens here.
type statusJSON struct {
	Version       string        `json:"version"`
	Uptime        string        `json:"uptime"`
	State         string        `json:"state"`
	Fault         string        `json:"fault"`
	LastFetch     string        `json:"lastFetch"`
	HasSnapshot   bool          `json:"hasSnapshot"`
	IPs           []string      `json:"ips"`
	Events        []eventJSON   `json:"events"`
	SoakActive    bool          `json:"soakActive"`
	SoakRemaining string        `json:"soakRemaining"`
	Update        update.Status `json:"update"`
}

func toStatusJSON(st StatusData) statusJSON {
	events := make([]eventJSON, len(st.Events))
	for i, e := range st.Events {
		events[i] = toEventJSON(e)
	}
	return statusJSON{
		Version:       st.Version,
		Uptime:        st.Uptime.String(),
		State:         st.State,
		Fault:         st.Fault,
		LastFetch:     st.LastFetch.Format(rfc3339),
		HasSnapshot:   st.HasSnapshot,
		IPs:           st.IPs,
		Events:        events,
		SoakActive:    st.SoakRemaining > 0,
		SoakRemaining: st.SoakRemaining.String(),
		Update:        st.Update,
	}
}

// writeJSON encodes v as the response body with the given status and the
// application/json content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError writes the uniform {"error":"..."} shape.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

// handleAPIStatus is GET /api/status: the same data behind the status page,
// as JSON.
func (s *Server) handleAPIStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toStatusJSON(s.svc.Status()))
}

// handleAPIStation is GET /api/station?crs=XXX: an offline CRS-code →
// station-name lookup (internal/stations), used by the web UI to resolve
// codes as the user types. Deliberately public — no auth, no setupGate — so
// the pre-auth setup pages can use it too (see setupGate's exemption list).
func (s *Server) handleAPIStation(w http.ResponseWriter, r *http.Request) {
	crs := r.URL.Query().Get("crs")
	name, ok := stations.Name(crs)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown station code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"crs": strings.ToUpper(crs), "name": name})
}

// stationJSON is stations.Station's lowerCamel JSON projection.
type stationJSON struct {
	CRS  string `json:"crs"`
	Name string `json:"name"`
}

// handleAPIStations is GET /api/stations?q=<text>: offline station search by
// name or code (internal/stations.Search), best match first, at most 8.
// Public like /api/station — the pre-auth setup pages use it (see
// setupGate's exemption list).
func (s *Server) handleAPIStations(w http.ResponseWriter, r *http.Request) {
	res := stations.Search(r.URL.Query().Get("q"), 8)
	out := make([]stationJSON, 0, len(res))
	for _, st := range res {
		out = append(out, stationJSON{CRS: st.CRS, Name: st.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

// tocJSON is stations.TOC's lowerCamel JSON projection.
type tocJSON struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// handleAPITOCs is GET /api/tocs?q=: offline operator search; empty q
// returns the full table (~31 rows) which the web UI caches for name hints.
// Public + setupGate-exempt like /api/station(s).
func (s *Server) handleAPITOCs(w http.ResponseWriter, r *http.Request) {
	res := stations.TOCSearch(r.URL.Query().Get("q"), 40)
	out := make([]tocJSON, 0, len(res))
	for _, tc := range res {
		out = append(out, tocJSON{Code: tc.Code, Name: tc.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAPIConfigGet is GET /api/config: the redacted config, as JSON.
// config.Config already carries lowerCamel json tags for its own on-disk
// shape, so it is written directly rather than through a dedicated DTO.
func (s *Server) handleAPIConfigGet(w http.ResponseWriter, _ *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// configUpdateJSON is PUT /api/config's request body: the non-secret config
// plus the three write-only secret fields UpdateConfig accepts.
type configUpdateJSON struct {
	Config         config.Config `json:"config"`
	NewToken       string        `json:"newToken"`
	NewWifiPSK     string        `json:"newWifiPsk"`
	NewPassword    string        `json:"newPassword"`
	NewRTTPassword string        `json:"newRttPassword"`
}

// handleAPIConfigPut is PUT /api/config: decode, validate-and-save via
// Service.UpdateConfig (same as the HTML config sub-pages), then respond
// {"status":"applied"} and schedule Actions.Apply — mirroring a restart-
// triggering config save's success path exactly, just without a re-rendered
// form on failure (a JSON API instead gets a 400 with the validation error).
func (s *Server) handleAPIConfigPut(w http.ResponseWriter, r *http.Request) {
	var body configUpdateJSON
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	update := ConfigUpdate{
		Cfg:            body.Config,
		NewToken:       body.NewToken,
		NewWifiPSK:     body.NewWifiPSK,
		NewPassword:    body.NewPassword,
		NewRTTPassword: body.NewRTTPassword,
	}
	if err := s.svc.UpdateConfig(update); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
	s.scheduleApply()
}

// handleAPIEvents is GET /api/events: the ring's events, newest first,
// capped at maxStatusEvents — the same view Status() assembles for the
// status page's event feed.
func (s *Server) handleAPIEvents(w http.ResponseWriter, _ *http.Request) {
	st := s.svc.Status()
	events := make([]eventJSON, len(st.Events))
	for i, e := range st.Events {
		events[i] = toEventJSON(e)
	}
	writeJSON(w, http.StatusOK, events)
}

// handleAPIActionsRestart is POST /api/actions/restart: mirrors
// handleActionsRestart's behaviour (schedule Actions.Apply), replying with
// JSON instead of the applied page.
func (s *Server) handleAPIActionsRestart(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
	s.scheduleApply()
}

// handleAPIActionsReboot is POST /api/actions/reboot: mirrors
// handleActionsReboot's synchronous Reboot-then-respond behaviour (see that
// handler's doc comment for why it isn't delayed like Apply), replying with
// JSON instead of the rebooting page.
func (s *Server) handleAPIActionsReboot(w http.ResponseWriter, _ *http.Request) {
	if err := s.svc.act.Reboot(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rebooting"})
}

// soakStartJSON is POST /api/actions/soak's request body.
type soakStartJSON struct {
	Duration string `json:"duration"`
}

// handleAPIActionsSoak is POST /api/actions/soak: mirrors the HTML actions
// page's start-soak form. Duration validation lives in Service.StartSoak so
// both surfaces reject the same inputs.
func (s *Server) handleAPIActionsSoak(w http.ResponseWriter, r *http.Request) {
	var body soakStartJSON
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := s.svc.StartSoak(body.Duration); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "soaking"})
}

// handleAPIActionsSoakCancel is POST /api/actions/soak/cancel: mirrors the
// HTML cancel form. Idle cancel is a no-op 200, matching the HTML surface.
func (s *Server) handleAPIActionsSoakCancel(w http.ResponseWriter, _ *http.Request) {
	s.svc.CancelSoak()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// handleAPIActionsWifiRetry is POST /api/actions/wifi-retry: mirrors the
// HTML retry-now form (see handleActionsWifiRetry), replying with JSON
// instead of a redirect.
func (s *Server) handleAPIActionsWifiRetry(w http.ResponseWriter, _ *http.Request) {
	s.svc.WifiRetryNow()
	writeJSON(w, http.StatusOK, map[string]string{"status": "retrying"})
}

// handleAPIUpdateCheck is POST /api/actions/update/check: mirrors
// handleUpdateCheck's on-demand release check, replying with JSON instead of
// a redirect. A check failure follows handleAPIActionsReboot's convention
// for a service-level failure: a 500 with the uniform error shape.
func (s *Server) handleAPIUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.CheckForUpdate(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "checked"})
}

// handleAPIUpdateApply is POST /api/actions/update/apply: mirrors
// handleUpdateApply's stage-then-schedule-restart behaviour, replying with
// JSON instead of the applied page. Failure returns the uniform error shape
// (500, matching handleAPIActionsReboot's convention for a service-level
// failure) and does NOT schedule a restart — the current binary keeps
// running, exactly like the HTML handler's failure path.
func (s *Server) handleAPIUpdateApply(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.ApplyUpdate(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
	s.scheduleApply()
}

// responseBuffer is an in-memory http.ResponseWriter used by apiJSONErrors
// to capture a downstream handler's full response before deciding whether to
// rewrite it.
type responseBuffer struct {
	header      http.Header
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (b *responseBuffer) Header() http.Header { return b.header }

func (b *responseBuffer) WriteHeader(code int) {
	if !b.wroteHeader {
		b.status = code
		b.wroteHeader = true
	}
}

func (b *responseBuffer) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

// apiJSONErrors makes every /api/* response's error shape uniform JSON, even
// though the shared csrfProtect and rateLimit middleware (reused as-is from
// the HTML routes) write their 403/429 rejections as plain text via
// http.Error. requireAuth's own 401 is already {"error":"unauthorized"}
// JSON and passes through unchanged.
//
// It must be the OUTERMOST middleware in an API route's chain: it buffers
// the entire downstream response (across requireAuth, csrfProtect,
// rateLimit, and the handler) before copying it to the real
// ResponseWriter, rewriting the body only if the status is an error and the
// content type isn't already application/json.
func apiJSONErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &responseBuffer{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		dst := w.Header()
		for k, v := range rec.header {
			if k == "Content-Length" {
				continue
			}
			dst[k] = v
		}

		if rec.status >= 400 && !strings.HasPrefix(dst.Get("Content-Type"), "application/json") {
			msg := strings.TrimSpace(rec.body.String())
			body, _ := json.Marshal(apiError{Error: msg})
			dst.Set("Content-Type", "application/json")
			dst.Set("X-Content-Type-Options", "nosniff")
			dst.Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(rec.status)
			_, _ = w.Write(body)
			return
		}

		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
	})
}
