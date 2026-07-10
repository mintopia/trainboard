package web

import (
	"bufio"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/stations"
)

// applyDelay is how long after redirecting to /restarting the server waits
// before firing Actions.Apply. It lives here (not in the Actions.Apply
// implementation itself) because it is a UI concern: giving the browser time
// to receive the redirect and start rendering the /restarting wait page
// before the process restarts. Production's Actions.Apply is an immediate
// os.Exit, wired up by main.
const applyDelay = 500 * time.Millisecond

// --- Settings list + per-section pages --------------------------------------
//
// GET /config renders this settings list; the old monolith form (GET+POST
// /config, handleConfigGet/handleConfigPost, config.html) is gone entirely —
// every section (departures, display, network, updates, admin) now has its
// own sub-page. Each row links to a sub-page that owns a slice of
// config.Config and saves ONLY that slice: the handler loads the full
// current config via Service.ConfigRedacted, mutates just its own fields,
// and passes the WHOLE cfg back to Service.UpdateConfig — which is safe
// because UpdateConfig only ever re-persists Board, Layout, Powersaving,
// Wifi.SSID, and Update from ConfigUpdate.Cfg (never Darwin.Token or
// Wifi.PSK, which stay write-only via NewToken/NewWifiPSK), so a redacted
// round-trip through any of these handlers can't corrupt or leak a secret
// even though ConfigRedacted's Darwin.Token/Wifi.PSK values are
// "***REDACTED***" placeholders rather than the real secrets.

// configListPageData is GET /config's render data: just the section
// summaries, one row per settings sub-page.
type configListPageData struct {
	basePage
	Sections []configSectionSummary
}

// configSectionSummary is one row of the settings list: Slug builds the link
// (/config/{{Slug}}), Title is the row's heading, Summary is a one-line
// preview of that section's current values.
type configSectionSummary struct {
	Slug, Title, Summary string
}

// handleConfigList renders GET /config: the settings list, linking to every
// section's sub-page (departures, display, network, updates, admin).
func (s *Server) handleConfigList(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	s.render(w, "configList", configListPageData{
		basePage: s.pageBase(r, "config"),
		Sections: []configSectionSummary{
			{"departures", "Departures", summarizeDepartures(cfg)},
			{"display", "Display", summarizeDisplay(cfg)},
			{"network", "Network", summarizeNetwork(cfg)},
			{"updates", "Updates", summarizeUpdates(cfg)},
			{"admin", "Admin", "Password"},
		},
	})
}

// summarizeDepartures renders the departures section's one-line preview:
// origin (resolved to a station name where recognised) → destination, plus
// the service count. An unset origin (a still-provisioning device) reads as
// "Not set" rather than falling through to " · 0 services".
func summarizeDepartures(cfg config.Config) string {
	if cfg.Board.Origin == "" {
		return "Not set"
	}
	origin := cfg.Board.Origin
	if name, ok := stations.Name(origin); ok {
		origin = name
	}
	sum := origin
	if cfg.Board.Destination != "" {
		dest := cfg.Board.Destination
		if name, ok := stations.Name(dest); ok {
			dest = name
		}
		sum += " → " + dest
	}
	return fmt.Sprintf("%s · %d services", sum, cfg.Board.Services)
}

// summarizeDisplay renders the display section's one-line preview.
func summarizeDisplay(cfg config.Config) string {
	if !cfg.Powersaving.Enabled {
		return "Full brightness all day"
	}
	return fmt.Sprintf("Dim %s–%s · brightness %d", cfg.Powersaving.Start, cfg.Powersaving.End, cfg.Powersaving.Brightness)
}

// summarizeNetwork renders the network section's one-line preview. It never
// renders the WiFi PSK or Darwin token themselves — only whether a token is
// present — which is safe against ConfigRedacted's masking: a redacted
// token is "***REDACTED***" (non-empty), so a real stored secret still
// reads as "set" here.
func summarizeNetwork(cfg config.Config) string {
	ssid := cfg.Wifi.SSID
	if ssid == "" {
		ssid = "WiFi not set"
	}
	token := "Darwin token not set"
	if cfg.Darwin.Token != "" {
		token = "Darwin token set"
	}
	return ssid + " · " + token
}

// summarizeUpdates renders the updates section's one-line preview.
func summarizeUpdates(cfg config.Config) string {
	sum := cfg.Update.EffectiveChannel()
	if cfg.Update.AutoApply {
		sum += " · automatic overnight"
	}
	if cfg.Update.DisableChecks {
		sum += " · checks off"
	}
	return sum
}

// configDeparturesPageData is GET/POST /config/departures's render data.
// OriginName/DestinationName are resolved once at render time so the page
// shows a station name next to the CRS code without a round trip through
// htmx on first load; the htmx-driven /api/station lookup takes over from
// there as the user edits either field.
type configDeparturesPageData struct {
	basePage
	Cfg              config.Config
	Error            string
	PlatformsCSV     string
	TOCsCSV          string
	ReplacementsText string
	OriginName       string
	DestinationName  string
}

// handleConfigDeparturesGet renders the departures form pre-filled from the
// stored config.
func (s *Server) handleConfigDeparturesGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	s.renderConfigDepartures(w, r, cfg, formatReplacements(cfg.Board.Replacements), "")
}

// renderConfigDepartures builds configDeparturesPageData from cfg (which, on
// a validation failure, is the user's SUBMITTED values, not the stored
// ones — see handleConfigDeparturesPost) and renders the departures page.
// replacementsText is passed separately rather than derived from
// cfg.Board.Replacements because the two legitimately diverge on a
// replacements PARSE failure: cfg still holds the stored map (there is
// nothing valid to overwrite it with), but the textarea must echo the raw
// text the user actually typed so they can see and fix it. GET passes
// formatReplacements(stored map); POST always passes the submitted raw text.
func (s *Server) renderConfigDepartures(w http.ResponseWriter, r *http.Request, cfg config.Config, replacementsText, errMsg string) {
	d := configDeparturesPageData{
		basePage:         s.pageBase(r, "config"),
		Cfg:              cfg,
		Error:            errMsg,
		PlatformsCSV:     joinCSV(cfg.Board.Platforms),
		TOCsCSV:          joinCSV(cfg.Board.TOCs),
		ReplacementsText: replacementsText,
	}
	d.OriginName, _ = stations.Name(cfg.Board.Origin)
	d.DestinationName, _ = stations.Name(cfg.Board.Destination)
	s.render(w, "configDepartures", d)
}

// handleConfigDeparturesPost parses ONLY the board.* fields onto a freshly
// loaded copy of the stored config (so Layout/Powersaving/Wifi/Update/Darwin
// pass through untouched — see this section's top-of-file doc comment) and
// saves it. Every field is parsed unconditionally, mirroring
// parseConfigForm's "only the first error wins, but every field still
// populates cfg" contract, so a re-render on error preserves every value the
// user typed. Origin is additionally checked against the offline stations
// table (not just isCRS's 3-letter-shape check inside config.Validate) so an
// unrecognised code gets a friendly, code-naming error instead of the
// generic CRS-shape one.
func (s *Server) handleConfigDeparturesPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}

	var firstErr error
	keepFirst := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	cfg.Board.Origin = strings.ToUpper(strings.TrimSpace(r.PostFormValue("board.origin")))
	cfg.Board.Destination = strings.ToUpper(strings.TrimSpace(r.PostFormValue("board.destination")))
	cfg.Board.Platforms = splitCSV(r.PostFormValue("board.platforms"))
	cfg.Board.TOCs = splitCSV(r.PostFormValue("board.tocs"))

	var perr error
	cfg.Board.Services, perr = parseIntField(r, "board.services")
	keepFirst(perr)
	cfg.Board.CutoffHours, perr = parseIntField(r, "board.cutoffHours")
	keepFirst(perr)
	cfg.Board.RefreshSeconds, perr = parseIntField(r, "board.refreshSeconds")
	keepFirst(perr)
	cfg.Board.TimeWindowMinutes, perr = parseIntField(r, "board.timeWindowMinutes")
	keepFirst(perr)

	rawReps := r.PostFormValue("board.replacements")
	reps, perr := parseReplacements(rawReps)
	keepFirst(perr)
	if perr == nil {
		cfg.Board.Replacements = reps
	}

	if _, ok := stations.Name(cfg.Board.Origin); !ok && firstErr == nil {
		firstErr = fmt.Errorf("%q is not a station code we recognise — 3 letters, e.g. PAD", cfg.Board.Origin)
	}

	if firstErr == nil {
		if err := s.svc.UpdateConfig(ConfigUpdate{Cfg: cfg}); err != nil {
			// An AP-provisioned device saving Departures FIRST (before ever
			// visiting Network) has no Darwin token yet, so UpdateConfig's
			// full Validate rejects even a perfectly valid origin on a field
			// this page doesn't carry. Echoing the bare "config: darwin.token
			// is required" would name a field with no home here and no hint
			// where its home is — replace it with a page-directing message.
			// Detected narrowly (token empty pre-save AND the error names
			// darwin.token) so every other validation error passes through
			// verbatim. cfg is redacted, but Redacted keeps an empty token
			// empty, so the emptiness check is faithful to what's stored.
			if cfg.Darwin.Token == "" && strings.Contains(err.Error(), "darwin.token") {
				err = fmt.Errorf("a Darwin API token is required before the board can run — nothing was saved; set the token on the Network page, where you can also set your station in one go")
			}
			firstErr = err
		}
	}
	if firstErr != nil {
		// rawReps, not formatReplacements(cfg.Board.Replacements): on a
		// replacements parse failure cfg still holds the STORED map, and the
		// re-render must echo what the user typed (see renderConfigDepartures).
		s.renderConfigDepartures(w, r, cfg, rawReps, firstErr.Error())
		return
	}
	s.scheduleApply()
	http.Redirect(w, r, "/restarting", http.StatusSeeOther)
}

// configDisplayPageData is GET/POST /config/display's render data.
type configDisplayPageData struct {
	basePage
	Cfg   config.Config
	Error string
}

// handleConfigDisplayGet renders the display form pre-filled from the stored
// config.
func (s *Server) handleConfigDisplayGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	s.renderConfigDisplay(w, r, cfg, "")
}

// renderConfigDisplay builds configDisplayPageData and renders the display
// page — see renderConfigDepartures's doc comment for why cfg carries the
// user's submitted values (not the stored ones) on a validation failure.
func (s *Server) renderConfigDisplay(w http.ResponseWriter, r *http.Request, cfg config.Config, errMsg string) {
	s.render(w, "configDisplay", configDisplayPageData{
		basePage: s.pageBase(r, "config"),
		Cfg:      cfg,
		Error:    errMsg,
	})
}

// handleConfigDisplayPost parses ONLY the powersaving.* and layout.times
// fields onto a freshly loaded copy of the stored config and saves it — see
// this section's top-of-file doc comment for why that's safe for the fields
// it doesn't touch.
func (s *Server) handleConfigDisplayPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}

	cfg.Powersaving.Enabled = formHasKey(r, "powersaving.enabled")
	cfg.Powersaving.Start = strings.TrimSpace(r.PostFormValue("powersaving.start"))
	cfg.Powersaving.End = strings.TrimSpace(r.PostFormValue("powersaving.end"))
	cfg.Layout.Times = formHasKey(r, "layout.times")

	brightness, perr := parseIntField(r, "powersaving.brightness")
	cfg.Powersaving.Brightness = brightness

	if perr == nil {
		perr = s.svc.UpdateConfig(ConfigUpdate{Cfg: cfg})
	}
	if perr != nil {
		s.renderConfigDisplay(w, r, cfg, perr.Error())
		return
	}
	s.scheduleApply()
	http.Redirect(w, r, "/restarting", http.StatusSeeOther)
}

// --- Network, Updates, Admin sub-pages --------------------------------------
//
// These three complete the settings-list pattern started by
// departures/display (Task 6): each handler loads the full current config
// via Service.ConfigRedacted, mutates ONLY its own slice of fields, and
// passes the whole cfg back to Service.UpdateConfig — see this file's
// top-of-file doc comment for why that round trip is safe.
//
// Unlike departures/display, a save here does not uniformly restart the
// board: Network and Updates do (a WiFi/Darwin credential change needs the
// connectivity manager to re-read it, and update.Checker snapshots its
// config at construction — checker.go:69), but Admin does not — VerifyLogin
// re-reads the password hash from disk on every login attempt
// (service.go:322-337), so a new password is live immediately with no
// restart needed. This is the one place scheduleApply() is deliberately NOT
// called after a successful save.

// configNetworkPageData is GET/POST /config/network's render data. Wifi.PSK
// and Darwin.Token are deliberately never referenced by config_network.html
// (see renderConfigNetwork's doc comment) — only Cfg.Wifi.SSID (not a
// secret) pre-fills. NeedsOrigin controls the page's one exception to being
// a pure "network" page — see handleConfigNetworkPost's doc comment for why.
type configNetworkPageData struct {
	basePage
	Cfg         config.Config
	NeedsOrigin bool
	Error       string
}

// handleConfigNetworkGet renders the network form pre-filled from the stored
// config.
func (s *Server) handleConfigNetworkGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	s.renderConfigNetwork(w, r, cfg, cfg.Board.Origin == "", "")
}

// renderConfigNetwork builds configNetworkPageData and renders the network
// page. cfg's Wifi.PSK/Darwin.Token are whatever ConfigRedacted or the
// caller's own mutation left them as (a redacted placeholder or blank) — the
// template must NEVER put either in a value attribute, since a redacted
// placeholder is not a secret worth protecting but IS confusing to submit
// back verbatim, and the real secret must never round-trip at all. See
// renderConfigDepartures's doc comment for why cfg carries the user's
// submitted (not stored) values on a validation failure.
//
// needsOrigin is passed separately rather than derived from
// cfg.Board.Origin=="" here, because the two diverge on a POST error
// re-render: by the time handleConfigNetworkPost calls this, cfg.Board.Origin
// already holds whatever the user just typed (so it echoes back correctly —
// see that handler), which is exactly the case a naive re-derivation would
// get backwards: an origin that was blank at load time but now holds a
// (possibly still-invalid) typed value must keep showing the field, not hide
// it because it's no longer literally empty.
func (s *Server) renderConfigNetwork(w http.ResponseWriter, r *http.Request, cfg config.Config, needsOrigin bool, errMsg string) {
	s.render(w, "configNetwork", configNetworkPageData{
		basePage:    s.pageBase(r, "config"),
		Cfg:         cfg,
		NeedsOrigin: needsOrigin,
		Error:       errMsg,
	})
}

// handleConfigNetworkPost parses wifi.ssid onto a freshly loaded copy of the
// stored config and threads wifi.psk/darwin.token through as
// ConfigUpdate's write-only NewWifiPSK/NewToken (blank means "keep the
// stored secret" — see ConfigUpdate's doc comment), then saves it and
// restarts: the connectivity manager and Darwin client both need a restart
// to pick up new credentials.
//
// It also accepts board.origin, but ONLY while nothing is stored yet. A
// device that only completed AP-mode partial setup has no Board.Origin yet
// (handleSetupPostAPMode collects WiFi + password only, deliberately leaving
// origin/token for later — see its doc comment), and UpdateConfig's full
// Validate requires Origin and a Darwin token together. Since Origin lives
// on the Departures page and Token lives here, neither page could otherwise
// ever complete first-boot provisioning alone: saving either one first would
// still fail Validate on the other's still-blank field.
// config_network.html renders the origin field solely in that gap
// (NeedsOrigin), so a normal edit — Origin already set — never submits or
// touches it.
func (s *Server) handleConfigNetworkPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	needsOrigin := cfg.Board.Origin == ""
	cfg.Wifi.SSID = strings.TrimSpace(r.PostFormValue("wifi.ssid"))
	if needsOrigin {
		if origin := strings.ToUpper(strings.TrimSpace(r.PostFormValue("board.origin"))); origin != "" {
			cfg.Board.Origin = origin
			// Mirror handleConfigDeparturesPost's check against the offline
			// stations table: config.Validate's isCRS only checks the
			// 3-letter shape, so without this a typo like "ZZZ" would save
			// (and restart the board into a fetch error) instead of getting
			// the friendly, code-naming rejection the Departures page gives.
			if _, ok := stations.Name(origin); !ok {
				s.renderConfigNetwork(w, r, cfg, needsOrigin,
					fmt.Sprintf("%q is not a station code we recognise — 3 letters, e.g. PAD", origin))
				return
			}
		}
	}

	upd := ConfigUpdate{
		Cfg:        cfg,
		NewWifiPSK: r.PostFormValue("wifi.psk"),
		NewToken:   r.PostFormValue("darwin.token"),
	}
	if err := s.svc.UpdateConfig(upd); err != nil {
		s.renderConfigNetwork(w, r, cfg, needsOrigin, err.Error())
		return
	}
	s.scheduleApply()
	http.Redirect(w, r, "/restarting", http.StatusSeeOther)
}

// configUpdatesPageData is GET/POST /config/updates's render data.
type configUpdatesPageData struct {
	basePage
	Cfg   config.Config
	Error string
}

// handleConfigUpdatesGet renders the updates form pre-filled from the stored
// config.
func (s *Server) handleConfigUpdatesGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	s.renderConfigUpdates(w, r, cfg, "")
}

// renderConfigUpdates builds configUpdatesPageData and renders the updates
// page — see renderConfigDepartures's doc comment for why cfg carries the
// user's submitted values (not the stored ones) on a validation failure.
func (s *Server) renderConfigUpdates(w http.ResponseWriter, r *http.Request, cfg config.Config, errMsg string) {
	s.render(w, "configUpdates", configUpdatesPageData{
		basePage: s.pageBase(r, "config"),
		Cfg:      cfg,
		Error:    errMsg,
	})
}

// handleConfigUpdatesPost parses ONLY the update.* fields onto a freshly
// loaded copy of the stored config and saves it. update.checks inverts into
// DisableChecks (the checkbox reads as "checks ON"; storage is the negation
// — see config.UpdateConfig's doc comment). A save here restarts the board:
// update.Checker snapshots its config once at construction (checker.go:69),
// so a running process never sees a channel/auto-apply/checks change made
// through this page without one.
func (s *Server) handleConfigUpdatesPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}

	cfg.Update.Channel = r.PostFormValue("update.channel")
	cfg.Update.AutoApply = formHasKey(r, "update.autoApply")
	cfg.Update.DisableChecks = !formHasKey(r, "update.checks")

	if err := s.svc.UpdateConfig(ConfigUpdate{Cfg: cfg}); err != nil {
		s.renderConfigUpdates(w, r, cfg, err.Error())
		return
	}
	s.scheduleApply()
	http.Redirect(w, r, "/restarting", http.StatusSeeOther)
}

// configAdminPageData is GET/POST /config/admin's render data. It carries no
// Cfg: the admin page has nothing to pre-fill (the password fields are
// write-only and never round-trip — see config_admin.html), so basePage plus
// an optional error is everything it needs.
type configAdminPageData struct {
	basePage
	Error string
}

// handleConfigAdminGet renders the (empty) admin form.
func (s *Server) handleConfigAdminGet(w http.ResponseWriter, r *http.Request) {
	s.render(w, "configAdmin", configAdminPageData{basePage: s.pageBase(r, "config")})
}

// handleConfigAdminPost validates the password/confirm match, then saves via
// ConfigUpdate.NewPassword against a freshly loaded copy of the stored
// config (Cfg unchanged — this page owns no config.Config fields, only the
// write-only password). Deliberately does NOT call scheduleApply: unlike
// every other section, VerifyLogin re-reads the password hash from disk on
// every login attempt (service.go:322-337), so the new password is live the
// instant this save returns — restarting the board would only interrupt
// live departures for no benefit.
func (s *Server) handleConfigAdminPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	pw := r.PostFormValue("web.password")
	confirm := r.PostFormValue("web.password.confirm")
	if pw != confirm {
		s.render(w, "configAdmin", configAdminPageData{basePage: s.pageBase(r, "config"), Error: "web.password and web.password.confirm do not match"})
		return
	}

	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	if err := s.svc.UpdateConfig(ConfigUpdate{Cfg: cfg, NewPassword: pw}); err != nil {
		s.render(w, "configAdmin", configAdminPageData{basePage: s.pageBase(r, "config"), Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/config", http.StatusSeeOther)
}

// parseIntField parses form field name as a base-10 integer, returning a
// form-display-friendly error (naming the field) on failure.
func parseIntField(r *http.Request, name string) (int, error) {
	v := strings.TrimSpace(r.PostFormValue(name))
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a whole number", name, v)
	}
	return n, nil
}

// formHasKey reports whether a checkbox's form key was submitted at all — an
// absent key means the checkbox was unchecked (false), matching HTML form
// semantics.
func formHasKey(r *http.Request, name string) bool {
	_, ok := r.PostForm[name]
	return ok
}

// splitCSV splits a comma-separated string into its trimmed, non-empty
// parts. An input with no non-empty parts returns nil.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinCSV is splitCSV's inverse for pre-filling the form: items joined with
// ", " in their existing (already-deterministic) slice order.
func joinCSV(items []string) string {
	return strings.Join(items, ", ")
}

// parseReplacements parses the replacements textarea: one "from=to" pair per
// non-blank line. A line without an "=" or with an empty "from" is rejected
// with an error naming the offending line, so the form can display it
// verbatim.
func parseReplacements(s string) (map[string]string, error) {
	out := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			return nil, fmt.Errorf("board.replacements: invalid line %q (want from=to)", line)
		}
		from := strings.TrimSpace(line[:idx])
		to := strings.TrimSpace(line[idx+1:])
		if from == "" {
			return nil, fmt.Errorf("board.replacements: invalid line %q (empty \"from\")", line)
		}
		out[from] = to
	}
	return out, nil
}

// formatReplacements is parseReplacements' inverse for pre-filling the
// textarea: one "from=to" line per entry, sorted by key for a deterministic
// rendering (map iteration order is not).
func formatReplacements(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+m[k])
	}
	return strings.Join(lines, "\n")
}
