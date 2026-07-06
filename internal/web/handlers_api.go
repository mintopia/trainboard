package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
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
	Version     string      `json:"version"`
	Uptime      string      `json:"uptime"`
	State       string      `json:"state"`
	Fault       string      `json:"fault"`
	LastFetch   string      `json:"lastFetch"`
	HasSnapshot bool        `json:"hasSnapshot"`
	IPs         []string    `json:"ips"`
	Events      []eventJSON `json:"events"`
}

func toStatusJSON(st StatusData) statusJSON {
	events := make([]eventJSON, len(st.Events))
	for i, e := range st.Events {
		events[i] = toEventJSON(e)
	}
	return statusJSON{
		Version:     st.Version,
		Uptime:      st.Uptime.String(),
		State:       st.State,
		Fault:       st.Fault,
		LastFetch:   st.LastFetch.Format(rfc3339),
		HasSnapshot: st.HasSnapshot,
		IPs:         st.IPs,
		Events:      events,
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
	Config      config.Config `json:"config"`
	NewToken    string        `json:"newToken"`
	NewWifiPSK  string        `json:"newWifiPsk"`
	NewPassword string        `json:"newPassword"`
}

// handleAPIConfigPut is PUT /api/config: decode, validate-and-save via
// Service.UpdateConfig (same as the HTML config form), then respond
// {"status":"applied"} and schedule Actions.Apply — mirroring
// handleConfigPost's success path exactly, just without a re-rendered form
// on failure (a JSON API instead gets a 400 with the validation error).
func (s *Server) handleAPIConfigPut(w http.ResponseWriter, r *http.Request) {
	var body configUpdateJSON
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	update := ConfigUpdate{
		Cfg:         body.Config,
		NewToken:    body.NewToken,
		NewWifiPSK:  body.NewWifiPSK,
		NewPassword: body.NewPassword,
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
			dst.Del("X-Content-Type-Options")
			dst.Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(rec.status)
			_, _ = w.Write(body)
			return
		}

		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
	})
}
