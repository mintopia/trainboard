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
)

// applyDelay is how long after rendering the applied page the server waits
// before firing Actions.Apply. It lives here (not in the Actions.Apply
// implementation itself) because it is a UI concern: giving the browser time
// to receive and render the "Saved" response before the process restarts.
// Production's Actions.Apply is an immediate os.Exit, wired up by main.
const applyDelay = 500 * time.Millisecond

// configPageData is the render data for the config editor: basePage
// (nav/CSRF), the config to pre-fill from (secrets never populated — see
// config.html, which never references Cfg.Darwin.Token/Cfg.Wifi.PSK), any
// validation error to display, and the stringified forms of the fields that
// don't map 1:1 onto an HTML input (CSV lists, the replacements map).
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
		basePage:         basePage{LoggedIn: true, CSRF: csrfFrom(r)},
		Cfg:              cfg,
		Error:            errMsg,
		PlatformsCSV:     joinCSV(cfg.Board.Platforms),
		TOCsCSV:          joinCSV(cfg.Board.TOCs),
		ReplacementsText: formatReplacements(cfg.Board.Replacements),
	})
}

// handleConfigGet renders the config form pre-filled from the stored config
// with secrets redacted. config.html never renders a value for the secret
// inputs regardless (they carry no value attribute at all), but Redacted is
// used anyway as defence in depth against a future template change.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderConfig(w, r, cfg, "")
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

	update := ConfigUpdate{
		Cfg:         cfg,
		NewToken:    r.PostFormValue("darwin.token"),
		NewWifiPSK:  r.PostFormValue("wifi.psk"),
		NewPassword: newPassword,
	}
	if err := s.svc.UpdateConfig(update); err != nil {
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
