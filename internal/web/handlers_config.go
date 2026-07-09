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

// applyDelay is how long after rendering the applied page the server waits
// before firing Actions.Apply. It lives here (not in the Actions.Apply
// implementation itself) because it is a UI concern: giving the browser time
// to receive and render the "Saved" response before the process restarts.
// Production's Actions.Apply is an immediate os.Exit, wired up by main.
const applyDelay = 500 * time.Millisecond

// --- Settings list + per-section pages (departures, display) ---------------
//
// GET /config now renders this settings list instead of the old monolith
// form; that form's GET handler (handleConfigGet) is gone, and only
// handleConfigPost/renderConfig/configPageData/config.html survive below for
// POST /config's save + error re-render path until Task 7 retires them — see
// configPageData's doc comment. Each row links to a
// sub-page that owns a slice of config.Config and saves ONLY that slice: the
// handler loads the full current config via Service.ConfigRedacted, mutates
// just its own fields, and passes the WHOLE cfg back to Service.UpdateConfig
// — which is safe because UpdateConfig only ever re-persists Board, Layout,
// Powersaving, Wifi.SSID, and Update from ConfigUpdate.Cfg (never
// Darwin.Token or Wifi.PSK, which stay write-only via NewToken/NewWifiPSK),
// so a redacted round-trip through this handler can't corrupt or leak a
// secret even though ConfigRedacted's Darwin.Token/Wifi.PSK values are
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

// handleConfigList renders GET /config: the settings list. Network, Updates,
// and Admin are listed (and linked) even though their sub-pages don't exist
// until Task 7 — the link and summary are harmless ahead of that page
// existing (they simply 404 until then), and listing every section here now
// means Task 7 only has to add the page, not touch this list.
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
		firstErr = s.svc.UpdateConfig(ConfigUpdate{Cfg: cfg})
	}
	if firstErr != nil {
		// rawReps, not formatReplacements(cfg.Board.Replacements): on a
		// replacements parse failure cfg still holds the STORED map, and the
		// re-render must echo what the user typed (see renderConfigDepartures).
		s.renderConfigDepartures(w, r, cfg, rawReps, firstErr.Error())
		return
	}
	s.scheduleApply()
	// TODO(task-8): redirect to /restarting once that page exists.
	http.Redirect(w, r, "/config", http.StatusSeeOther)
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
	// TODO(task-8): redirect to /restarting once that page exists.
	http.Redirect(w, r, "/config", http.StatusSeeOther)
}

// --- Old monolith config editor (POST /config only — see doc comments) -----

// configPageData is the render data for the OLD monolith config editor:
// basePage (nav/CSRF), the config to pre-fill from (secrets never populated
// — see config.html, which never references Cfg.Darwin.Token/Cfg.Wifi.PSK),
// any validation error to display, and the stringified forms of the fields
// that don't map 1:1 onto an HTML input (CSV lists, the replacements map).
//
// GET /config no longer routes to this page (handleConfigList — the
// settings list — took over that route this task); POST /config still does,
// since network/updates/admin fields have no dedicated sub-page yet and this
// remains the only way to save them from the HTML surface until Task 7
// retires this whole file section alongside config.html.
type configPageData struct {
	basePage
	Cfg              config.Config
	Error            string
	PlatformsCSV     string
	TOCsCSV          string
	ReplacementsText string
}

// renderConfig builds configPageData from cfg and renders the config page.
func (s *Server) renderConfig(w http.ResponseWriter, r *http.Request, cfg config.Config, errMsg string) {
	s.render(w, "config", configPageData{
		basePage:         basePage{LoggedIn: true, CSRF: csrfFrom(r), Active: "config"},
		Cfg:              cfg,
		Error:            errMsg,
		PlatformsCSV:     joinCSV(cfg.Board.Platforms),
		TOCsCSV:          joinCSV(cfg.Board.TOCs),
		ReplacementsText: formatReplacements(cfg.Board.Replacements),
	})
}

// handleConfigPost parses the submitted form into a ConfigUpdate and applies
// it. On any parse or validation failure it re-renders the form (200) with
// the error and the user's submitted non-secret values, and Actions.Apply is
// never fired. On success it renders the applied page and schedules
// Actions.Apply after applyDelay, once the response has been written.
func (s *Server) handleConfigPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	cfg, err := parseConfigForm(r)
	if err != nil {
		s.renderConfig(w, r, cfg, err.Error())
		return
	}

	newPassword := r.PostFormValue("web.password")
	confirm := r.PostFormValue("web.password.confirm")
	if newPassword != confirm {
		s.renderConfig(w, r, cfg, "web.password and web.password.confirm do not match")
		return
	}

	upd := ConfigUpdate{
		Cfg:         cfg,
		NewToken:    r.PostFormValue("darwin.token"),
		NewWifiPSK:  r.PostFormValue("wifi.psk"),
		NewPassword: newPassword,
	}
	if err := s.svc.UpdateConfig(upd); err != nil {
		s.renderConfig(w, r, cfg, err.Error())
		return
	}

	s.render(w, "applied", basePage{LoggedIn: true, CSRF: csrfFrom(r)})
	s.scheduleApply()
}

// parseConfigForm reads the non-secret config.Config fields the config form
// submits (Board, Layout, Powersaving, Wifi.SSID — the fields
// Service.UpdateConfig actually merges from ConfigUpdate.Cfg). Secrets
// (darwin.token, wifi.psk, web.password) are read directly by the caller
// into ConfigUpdate's write-only fields instead.
//
// Every field is parsed unconditionally, regardless of whether an earlier
// field failed: only the FIRST parse error is returned, but cfg is always
// populated from every field that parsed successfully. This matters because
// the caller re-renders the form from the returned cfg on error — bailing
// out on the first failure would silently revert every later field to its
// zero value in that re-render, discarding user input the form never had a
// problem with.
func parseConfigForm(r *http.Request) (config.Config, error) {
	var cfg config.Config
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

	var err error
	cfg.Board.Services, err = parseIntField(r, "board.services")
	keepFirst(err)
	cfg.Board.CutoffHours, err = parseIntField(r, "board.cutoffHours")
	keepFirst(err)
	cfg.Board.RefreshSeconds, err = parseIntField(r, "board.refreshSeconds")
	keepFirst(err)
	cfg.Board.TimeWindowMinutes, err = parseIntField(r, "board.timeWindowMinutes")
	keepFirst(err)

	reps, err := parseReplacements(r.PostFormValue("board.replacements"))
	keepFirst(err)
	if err == nil {
		cfg.Board.Replacements = reps
	}

	cfg.Layout.Times = formHasKey(r, "layout.times")

	cfg.Powersaving.Enabled = formHasKey(r, "powersaving.enabled")
	cfg.Powersaving.Start = strings.TrimSpace(r.PostFormValue("powersaving.start"))
	cfg.Powersaving.End = strings.TrimSpace(r.PostFormValue("powersaving.end"))
	cfg.Powersaving.Brightness, err = parseIntField(r, "powersaving.brightness")
	keepFirst(err)

	cfg.Wifi.SSID = strings.TrimSpace(r.PostFormValue("wifi.ssid"))

	cfg.Update.Channel = r.PostFormValue("update.channel")
	cfg.Update.AutoApply = formHasKey(r, "update.autoApply")
	cfg.Update.DisableChecks = !formHasKey(r, "update.checks") // checkbox is "checks ON"; storage is inverted (see config.UpdateConfig)

	return cfg, firstErr
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
