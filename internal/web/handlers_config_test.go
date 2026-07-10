package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

// configTestPassword is the admin password newConfigTestServer sets up.
const configTestPassword = "longenough1"

// configTestToken is the Darwin token stored by newConfigTestServer's baseline
// config — a value distinctive enough that its accidental presence anywhere
// in a rendered page is unambiguous.
const configTestToken = "tok-super-secret-xyz"

// connFakes tracks connectivity seam state for testing, backed by plain vars.
type connFakes struct {
	hs        *board.Hotspot
	lastErr   string
	retries   int
	provNotes int
	mu        sync.Mutex
}

// set updates the hotspot and error state.
func (cf *connFakes) set(hs *board.Hotspot, err string) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	cf.hs = hs
	cf.lastErr = err
}

// counts returns the current retry and provisioning note counts.
func (cf *connFakes) counts() (retries, provNotes int) {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	return cf.retries, cf.provNotes
}

// newConfigTestServerCore is the shared construction path behind
// newConfigTestServer and newConnTestServer: it saves a baseline config
// (origin PAD, a known Darwin token), wires Sources/Actions — folding in the
// connectivity seam fakes when conn is non-nil — constructs the
// Service/Server, and sets the initial admin password. conn wiring happens
// before NewService/NewServer so the seam is live from the moment the Server
// exists.
func newConfigTestServerCore(t *testing.T, conn *connFakes) (srv *Server, svc *Service, path string, applyCh chan struct{}) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	cfg.Board.Origin = "PAD"
	cfg.Darwin.Token = configTestToken
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	applyCh = make(chan struct{}, 1)
	var soakRem time.Duration // harness soak fake: StartSoak stores, Cancel zeroes
	src := Sources{
		Snapshot:      func() *board.Snapshot { return nil },
		Ring:          obs.NewRing(8),
		Version:       "vtest",
		StartedAt:     time.Now(),
		SoakRemaining: func() time.Duration { return soakRem },
	}
	act := Actions{
		Apply:      func() { applyCh <- struct{}{} },
		Reboot:     func() error { return nil },
		SoakStart:  func(d time.Duration) { soakRem = d },
		SoakCancel: func() { soakRem = 0 },
	}
	if conn != nil {
		src.Hotspot = func() *board.Hotspot {
			conn.mu.Lock()
			defer conn.mu.Unlock()
			return conn.hs
		}
		src.LastSTAError = func() string {
			conn.mu.Lock()
			defer conn.mu.Unlock()
			return conn.lastErr
		}
		act.WifiRetry = func() {
			conn.mu.Lock()
			defer conn.mu.Unlock()
			conn.retries++
		}
		act.NoteProvisioning = func() {
			conn.mu.Lock()
			defer conn.mu.Unlock()
			conn.provNotes++
		}
	}
	svc = NewService(path, src, act, testLog())
	if err := svc.SetInitialPassword(configTestPassword, "PAD", ""); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	srv = NewServer(svc, testLog())
	return srv, svc, path, applyCh
}

// newConnTestServer shares newConfigTestServer's setup (both call the
// newConfigTestServerCore builder) and additionally returns access to the
// connectivity seam fakes (hotspot state, last error, retry/provisioning
// counts) wired into Sources/Actions before the Server was constructed.
func newConnTestServer(t *testing.T) (srv *Server, svc *Service, path string, applyCh chan struct{}, conn *connFakes) {
	t.Helper()
	conn = &connFakes{}
	srv, svc, path, applyCh = newConfigTestServerCore(t, conn)
	return srv, svc, path, applyCh, conn
}

// newConfigTestServer wires a Server over a valid, saved baseline config
// (origin PAD, a known Darwin token, admin password already set) and returns
// the server, its Service, the config file path, and a channel that receives
// a value each time the wired Actions.Apply fires.
func newConfigTestServer(t *testing.T) (srv *Server, svc *Service, path string, applyCh chan struct{}) {
	t.Helper()
	return newConfigTestServerCore(t, nil)
}

// setBoardTOCs is a test helper that mutates the stored config's Board.TOCs
// directly via UpdateConfig, used to seed the test data for Operators-field
// tests.
func setBoardTOCs(t *testing.T, svc *Service, tocs []string) {
	t.Helper()
	cfg, err := svc.ConfigRedacted()
	if err != nil {
		t.Fatalf("setBoardTOCs: load config: %v", err)
	}
	cfg.Board.TOCs = tocs
	if err := svc.UpdateConfig(ConfigUpdate{Cfg: cfg}); err != nil {
		t.Fatalf("setBoardTOCs: save config: %v", err)
	}
}

// awaitApply waits up to 1s for a value on ch, failing the test if it never
// arrives.
func awaitApply(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("Actions.Apply was not called within 1s")
	}
}

// assertApplyNotCalled fails the test if ch has a pending value or receives
// one within a short grace window (long enough to catch an erroneous
// AfterFunc firing, short enough not to slow the suite down).
func assertApplyNotCalled(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("Actions.Apply must not be called")
	case <-time.After(50 * time.Millisecond):
	}
}

// (a) GET /config authed -> 200 (the settings list, this task). It must
// never leak the stored Darwin token — the list only ever renders section
// summaries, never a raw secret — and must link to every section, including
// the departures/display sub-pages this task adds.
func TestConfigGetRendersFormWithoutLeakingSecrets(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, configTestToken) {
		t.Fatalf("stored Darwin token leaked into config list page body: %s", body)
	}
	for _, want := range []string{"/config/departures", "/config/display"} {
		if !strings.Contains(body, want) {
			t.Errorf("config list missing link %q: %s", want, body)
		}
	}
}

// TestConfigListShowsSummaries pins the settings list's contract: every
// section is listed and linked, including network/updates/admin, which have
// no sub-page of their own until Task 7 (their links 404 until then — see
// handleConfigList's doc comment).
func TestConfigListShowsSummaries(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"/config/departures", "/config/display", "/config/network", "/config/updates", "/config/admin"} {
		if !strings.Contains(body, want) {
			t.Errorf("config list missing link %q", want)
		}
	}
	// The baseline config (origin PAD) resolves to a station name in the
	// departures summary.
	if !strings.Contains(body, "London Paddington") {
		t.Errorf("expected departures summary to resolve PAD to a station name: %s", body)
	}
}

// baseDeparturesForm returns a fresh, fully-populated, valid form matching
// the baseline config written by newConfigTestServer's departures fields
// (origin PAD, config.Default()'s Board defaults). Callers mutate the
// returned map per-test.
func baseDeparturesForm() url.Values {
	return url.Values{
		"board.origin":            {"PAD"},
		"board.destination":       {""},
		"board.platforms":         {""},
		"board.tocs":              {""},
		"board.services":          {"3"},
		"board.cutoffHours":       {"8"},
		"board.refreshSeconds":    {"60"},
		"board.timeWindowMinutes": {"120"},
		"board.replacements":      {""},
	}
}

// baseDisplayForm returns a fresh, fully-populated, valid form matching the
// baseline config written by newConfigTestServer's display fields
// (config.Default()'s Powersaving/Layout defaults).
func baseDisplayForm() url.Values {
	return url.Values{
		"powersaving.start":      {"23:00"},
		"powersaving.end":        {"07:00"},
		"powersaving.brightness": {"32"},
		"layout.times":           {"on"},
		// powersaving.enabled deliberately absent: config.Default() has it
		// false, and checkbox semantics say an absent key means false.
	}
}

// TestConfigDepartures covers GET pre-filling the departures form and a
// valid POST saving a changed origin, scheduling Actions.Apply.
func TestConfigDepartures(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config/departures", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="board.origin"`) {
		t.Fatalf("GET form: code %d body=%s", rec.Code, rec.Body.String())
	}

	form := baseDeparturesForm()
	form.Set("board.origin", "EUS")
	form.Set("board.destination", "MAN")
	form.Set("board.platforms", "1, 2")
	form.Set("board.tocs", "GW, XR")
	form.Set("board.refreshSeconds", "30")
	form.Set("csrf", csrf)
	rec = postForm(t, srv.Handler(), "/config/departures", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST: want 303, got %d: %s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Board.Origin != "EUS" || cur.Board.RefreshSeconds != 30 {
		t.Fatalf("Board = %+v, want origin EUS refreshSeconds 30", cur.Board)
	}
	if cur.Board.Destination != "MAN" {
		t.Fatalf("Board.Destination = %q, want MAN", cur.Board.Destination)
	}
	if !reflect.DeepEqual(cur.Board.Platforms, []string{"1", "2"}) {
		t.Fatalf("Board.Platforms = %#v, want [1 2]", cur.Board.Platforms)
	}
	if !reflect.DeepEqual(cur.Board.TOCs, []string{"GW", "XR"}) {
		t.Fatalf("Board.TOCs = %#v, want [GW XR]", cur.Board.TOCs)
	}
	// Darwin token (a different section) must pass through untouched.
	if cur.Darwin.Token != configTestToken {
		t.Fatalf("Darwin.Token = %q, want unchanged %q (departures POST must not touch other sections)", cur.Darwin.Token, configTestToken)
	}

	// GET must pre-fill every field from what was just saved, including the
	// resolved destination station name.
	recGet := getPath(t, srv.Handler(), "/config/departures", cookie)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET after save: want 200, got %d", recGet.Code)
	}
	body := recGet.Body.String()
	for _, want := range []string{`value="EUS"`, `value="MAN"`, `value="1, 2"`, `value="GW, XR"`, `value="30"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in re-rendered form: %s", want, body)
		}
	}
}

// TestDeparturesFormHasStationSuggest pins the CRS fields as suggest.js
// combobox enhancements (#62): data-suggest carries the search endpoint, and
// the legacy htmx per-keystroke lookup attributes are gone.
func TestDeparturesFormHasStationSuggest(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	body := getPath(t, srv.Handler(), "/config/departures", cookie).Body.String()
	for _, want := range []string{
		`data-suggest="/api/stations"`,
		`data-hint="origin-name"`,
		`data-hint="dest-name"`,
		`src="/static/suggest.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("departures form missing %q", want)
		}
	}
	if strings.Contains(body, `hx-get="/api/station"`) {
		t.Errorf("legacy htmx station lookup still present")
	}
}

// The Operators field carries TOC suggest + server-rendered name hints
// (#63): "GW, XR" renders "Great Western Railway, Elizabeth line".
func TestDeparturesFormTOCHints(t *testing.T) {
	srv, svc, _, _ := newConfigTestServer(t)
	setBoardTOCs(t, svc, []string{"GW", "XR"})
	cookie, _ := loginAs(t, srv, configTestPassword)

	body := getPath(t, srv.Handler(), "/config/departures", cookie).Body.String()
	for _, want := range []string{
		`data-suggest="/api/tocs"`,
		`data-multi=","`,
		`data-hint="tocs-names"`,
		`Great Western Railway, Elizabeth line`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("departures form missing %q", want)
		}
	}
}

// TestDeparturesTOCHintSurvivesValidationError pins the brief-flagged
// regression for the Operators hint (#63): a POST that fails validation
// elsewhere (bad origin CRS) must re-render with the typed TOC codes still
// resolved to names — a validation error must not blank the hint.
func TestDeparturesTOCHintSurvivesValidationError(t *testing.T) {
	srv, _, _, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := baseDeparturesForm()
	form.Set("board.origin", "XX") // fails validation: not a real station
	form.Set("board.tocs", "GW, XR")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/departures", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `Great Western Railway, Elizabeth line`) {
		t.Errorf("expected TOC name hints preserved in validation-error re-render: %s", body)
	}
	if !strings.Contains(body, `value="GW, XR"`) {
		t.Errorf("expected typed TOC codes preserved in re-rendered form: %s", body)
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigDeparturesValidationError covers an unrecognised origin CRS:
// re-render (200, not a redirect) with an error naming the station code, and
// the user's other typed values preserved.
func TestConfigDeparturesValidationError(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseDeparturesForm()
	form.Set("board.origin", "XX")
	form.Set("board.refreshSeconds", "90")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/departures", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "station code") {
		t.Errorf("expected a validation message naming the station code: %s", body)
	}
	if !strings.Contains(body, `value="90"`) {
		t.Errorf("expected refreshSeconds=90 preserved in re-rendered form: %s", body)
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged on validation error:\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigDeparturesValidationPreservesTypedReplacements covers the
// replacements textarea itself failing to parse: the error re-render must
// echo the user's typed (invalid) text back into the textarea, not the
// stored value — a parse failure is exactly when the user most needs to see
// what they actually typed in order to fix it.
func TestConfigDeparturesValidationPreservesTypedReplacements(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseDeparturesForm()
	form.Set("board.replacements", "badline")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/departures", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">badline</textarea>") {
		t.Errorf("expected the typed replacements text preserved in the textarea: %s", body)
	}
	if !strings.Contains(body, "board.replacements: invalid line") {
		t.Errorf("expected the replacements parse error message in the body: %s", body)
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged on validation error:\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// newPartialSetupTestServer wires a Server over a config in the AP-mode
// partial-setup state: admin password + WiFi creds stored (via
// SaveConnectivity), but NO Board.Origin and NO Darwin.Token yet — the
// connectivity-valid-but-board-invalid document a device has after
// handleSetupPostAPMode but before finish-provisioning.
func newPartialSetupTestServer(t *testing.T) (srv *Server, path string, applyCh chan struct{}) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "config.json")
	hash, err := HashPassword(configTestPassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	partial := config.Default() // empty origin/token: fails full Validate
	partial.Web.PasswordHash = hash
	partial.Wifi.SSID = "HomeNet"
	partial.Wifi.PSK = "supersecret"
	if err := config.SaveConnectivity(path, partial); err != nil {
		t.Fatalf("seed partial-setup config: %v", err)
	}
	applyCh = make(chan struct{}, 1)
	src := Sources{
		Snapshot:  func() *board.Snapshot { return nil },
		Ring:      obs.NewRing(8),
		Version:   "vtest",
		StartedAt: time.Now(),
	}
	act := Actions{
		Apply:  func() { applyCh <- struct{}{} },
		Reboot: func() error { return nil },
	}
	svc := NewService(path, src, act, testLog())
	return NewServer(svc, testLog()), path, applyCh
}

// TestConfigDeparturesFirstOnPartialSetupDirectsToNetwork covers the
// departures-first path on an AP-provisioned device (origin AND token both
// still empty): saving a perfectly valid origin still fails UpdateConfig's
// full Validate on the missing Darwin token — a field this page doesn't
// have. The re-render must direct the user to the Network page (where the
// token AND the origin can be set together in one save — see
// handleConfigNetworkPost's doc comment) instead of echoing config.Validate's
// bare "config: darwin.token is required", which names a field with no home
// here and no hint where its home is. Nothing is saved.
func TestConfigDeparturesFirstOnPartialSetupDirectsToNetwork(t *testing.T) {
	srv, path, applyCh := newPartialSetupTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.LoadRaw(path) // board-invalid on purpose; LoadRaw, not Load
	if err != nil {
		t.Fatal(err)
	}

	form := baseDeparturesForm() // valid origin (PAD), valid everything else
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/departures", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Network page") {
		t.Errorf("expected the error to direct the user to the Network page: %s", body)
	}
	if strings.Contains(body, "config: darwin.token is required") {
		t.Errorf("the raw validate error must be replaced, not echoed: %s", body)
	}

	after, err := config.LoadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged (nothing was saved):\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigNetworkConditionalOriginUnrecognised covers the Network page's
// conditional board.origin field (rendered only while no origin is stored —
// see handleConfigNetworkPost) being submitted with a code that isn't in the
// offline stations table: the same friendly, code-naming rejection the
// Departures page gives, with the typed value preserved, nothing saved, and
// no restart.
func TestConfigNetworkConditionalOriginUnrecognised(t *testing.T) {
	srv, path, applyCh := newPartialSetupTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.LoadRaw(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseNetworkForm()
	form.Set("board.origin", "ZZZ") // valid CRS shape, not a real station
	form.Set("darwin.token", "tok-finish")
	form.Set("wifi.ssid", "HomeNet") // mirrors the pre-filled, non-secret SSID input
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/network", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "not a station code we recognise") {
		t.Errorf("expected the friendly station-code rejection: %s", body)
	}
	if !strings.Contains(body, `value="ZZZ"`) {
		t.Errorf("expected the typed origin preserved in the re-rendered form: %s", body)
	}

	after, err := config.LoadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged on validation error:\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigDeparturesReplacementsRoundTrip covers the replacements textarea
// specifically (migrated from the old monolith's GET /config assertion, now
// that GET /config no longer renders it — see TestConfigGetRendersFormWithoutLeakingSecrets's
// updated doc comment).
func TestConfigDeparturesReplacementsRoundTrip(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := baseDeparturesForm()
	form.Set("board.replacements", "Bristol Temple Meads=Bristol TM")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/departures", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d body=%s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cur.Board.Replacements["Bristol Temple Meads"]; got != "Bristol TM" {
		t.Fatalf("Replacements[Bristol Temple Meads] = %q, want %q", got, "Bristol TM")
	}

	rec2 := getPath(t, srv.Handler(), "/config/departures", cookie)
	if rec2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "Bristol Temple Meads=Bristol TM") {
		t.Fatalf("expected replacements textarea to round-trip, got: %s", rec2.Body.String())
	}
}

// TestConfigDisplay covers GET pre-filling the display form and a valid POST
// saving changed powersaving/layout fields, scheduling Actions.Apply.
func TestConfigDisplay(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config/display", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="powersaving.brightness"`) {
		t.Fatalf("GET form: code %d body=%s", rec.Code, rec.Body.String())
	}

	form := baseDisplayForm()
	form.Set("powersaving.enabled", "on")
	form.Set("powersaving.start", "22:30")
	form.Set("powersaving.end", "06:15")
	form.Set("powersaving.brightness", "64")
	form.Del("layout.times")
	form.Set("csrf", csrf)
	rec = postForm(t, srv.Handler(), "/config/display", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST: want 303, got %d: %s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cur.Powersaving.Enabled || cur.Powersaving.Brightness != 64 {
		t.Fatalf("Powersaving = %+v, want Enabled=true Brightness=64", cur.Powersaving)
	}
	if cur.Powersaving.Start != "22:30" || cur.Powersaving.End != "06:15" {
		t.Fatalf("Powersaving window = %s-%s, want 22:30-06:15", cur.Powersaving.Start, cur.Powersaving.End)
	}
	if cur.Layout.Times {
		t.Fatalf("Layout.Times = true, want false (checkbox key absent)")
	}
	// Board (a different section) must pass through untouched.
	if cur.Board.Origin != "PAD" {
		t.Fatalf("Board.Origin = %q, want unchanged %q (display POST must not touch other sections)", cur.Board.Origin, "PAD")
	}

	// GET must pre-fill every field from what was just saved.
	recGet := getPath(t, srv.Handler(), "/config/display", cookie)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET after save: want 200, got %d", recGet.Code)
	}
	body := recGet.Body.String()
	for _, want := range []string{`name="powersaving.enabled" checked`, `value="22:30"`, `value="06:15"`, `value="64"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in re-rendered form: %s", want, body)
		}
	}
	if strings.Contains(body, `name="layout.times" checked`) {
		t.Errorf("expected layout.times unchecked in re-rendered form: %s", body)
	}
}

// TestConfigDisplayValidationError covers an invalid HH:MM value while
// powersaving is enabled: re-render (200) with the error, typed values
// preserved, file unchanged.
func TestConfigDisplayValidationError(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseDisplayForm()
	form.Set("powersaving.enabled", "on")
	form.Set("powersaving.start", "not-a-time")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/display", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "powersaving") {
		t.Errorf("expected a powersaving validation message: %s", body)
	}
	if !strings.Contains(body, `value="not-a-time"`) {
		t.Errorf("expected the invalid start value preserved in re-rendered form: %s", body)
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged on validation error:\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// baseNetworkForm returns a fresh, fully-populated, valid form matching the
// baseline config written by newConfigTestServer's network fields (no wifi
// configured, secrets left blank i.e. "keep the stored value").
func baseNetworkForm() url.Values {
	return url.Values{
		"wifi.ssid":    {""},
		"wifi.psk":     {""},
		"darwin.token": {""},
	}
}

// baseUpdatesForm returns a fresh, fully-populated, valid form matching the
// baseline config written by newConfigTestServer's update fields
// (config.Default()'s Update defaults: channel stable, checks on, autoApply
// off).
func baseUpdatesForm() url.Values {
	return url.Values{
		"update.channel": {"stable"},
		// update.checks deliberately present ("on"): its checkbox defaults to
		// checked (DisableChecks false). update.autoApply deliberately absent
		// (its default is unchecked/off).
		"update.checks": {"on"},
	}
}

// TestConfigNetwork covers GET pre-filling the network form (SSID only —
// secrets never pre-fill) and a valid POST saving a changed SSID, scheduling
// Actions.Apply because a network change can strand the board on the wrong
// WiFi.
func TestConfigNetwork(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config/network", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="wifi.ssid"`) {
		t.Fatalf("GET form: code %d body=%s", rec.Code, rec.Body.String())
	}

	// The baseline config has no WiFi configured at all (SSID and PSK both
	// blank): config.validateWifi requires the two together (or both
	// blank), so a first-time SSID needs a PSK supplied alongside it.
	form := baseNetworkForm()
	form.Set("wifi.ssid", "HomeNet")
	form.Set("wifi.psk", "somepassword1")
	form.Set("csrf", csrf)
	rec = postForm(t, srv.Handler(), "/config/network", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST: want 303, got %d: %s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Wifi.SSID != "HomeNet" {
		t.Fatalf("Wifi.SSID = %q, want HomeNet", cur.Wifi.SSID)
	}
	// Darwin token (a different secret) must pass through untouched.
	if cur.Darwin.Token != configTestToken {
		t.Fatalf("Darwin.Token = %q, want unchanged %q (network POST must not touch it when blank)", cur.Darwin.Token, configTestToken)
	}

	recGet := getPath(t, srv.Handler(), "/config/network", cookie)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET after save: want 200, got %d", recGet.Code)
	}
	if !strings.Contains(recGet.Body.String(), `value="HomeNet"`) {
		t.Errorf("expected wifi.ssid=HomeNet preserved in re-rendered form: %s", recGet.Body.String())
	}
}

// TestConfigNetworkSavesSecretsWriteOnly is task 7's pinned RED test: a new
// wifi.psk replaces the stored PSK, while a blank darwin.token keeps the
// stored token unchanged — secrets are write-only, and either one changing
// still restarts the board (Wifi.PSK and Darwin.Token both live in the same
// connectivity/data seams a restart is needed to re-read).
func TestConfigNetworkSavesSecretsWriteOnly(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := url.Values{"csrf": {csrf}, "wifi.ssid": {"NewNet"}, "wifi.psk": {"newpassword1"}, "darwin.token": {""}}
	rec := postForm(t, srv.Handler(), "/config/network", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh) // network changes restart

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Wifi.SSID != "NewNet" || cur.Wifi.PSK != "newpassword1" {
		t.Fatalf("Wifi = %+v, want ssid=NewNet psk=newpassword1", cur.Wifi)
	}
	if cur.Darwin.Token != configTestToken {
		t.Fatalf("Darwin.Token = %q, want unchanged %q (empty darwin.token means keep)", cur.Darwin.Token, configTestToken)
	}
}

// TestConfigNetworkValidationErrorPreservesTypedSSID covers an invalid PSK
// (too short) while a new SSID is also typed: re-render (200) with the
// error, the typed SSID preserved, the file unchanged, and Actions.Apply NOT
// called. It also confirms the invalid PSK itself is never echoed back.
func TestConfigNetworkValidationErrorPreservesTypedSSID(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseNetworkForm()
	form.Set("wifi.ssid", "TypedNet")
	form.Set("wifi.psk", "short")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/network", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "wifi.psk") {
		t.Errorf("expected a wifi.psk validation message: %s", body)
	}
	if !strings.Contains(body, `value="TypedNet"`) {
		t.Errorf("expected the typed SSID preserved in re-rendered form: %s", body)
	}
	if strings.Contains(body, "short") {
		t.Errorf("the invalid PSK itself must never be echoed back: %s", body)
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged on validation error:\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigNetworkSecretsNeverRoundTrip is the reinstated equivalent of the
// old monolith GET-page invariant this task's brief calls out: the network
// page's secret inputs (wifi.psk, darwin.token) must render with
// placeholder="unchanged" and NEVER a value attribute, even though a real
// token is stored — regardless of GET (pre-fill) or a POST error re-render.
func TestConfigNetworkSecretsNeverRoundTrip(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config/network", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, configTestToken) {
		t.Fatalf("stored Darwin token leaked into network page body: %s", body)
	}
	for _, want := range []string{`name="wifi.psk" placeholder="unchanged"`, `name="darwin.token" placeholder="unchanged"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in network page: %s", want, body)
		}
	}
	for _, secretField := range []string{"wifi.psk", "darwin.token"} {
		if strings.Contains(body, `name="`+secretField+`" value=`) {
			t.Fatalf("secret field %s must never render a value attribute: %s", secretField, body)
		}
	}

	// Trigger a validation-error re-render too — the same invariant must
	// hold there, since that path also has a live cfg to render from.
	form := baseNetworkForm()
	form.Set("wifi.psk", "short")
	form.Set("csrf", csrf)
	recErr := postForm(t, srv.Handler(), "/config/network", form, cookie)
	if recErr.Code != http.StatusOK {
		t.Fatalf("POST validation error: want 200, got %d", recErr.Code)
	}
	errBody := recErr.Body.String()
	for _, secretField := range []string{"wifi.psk", "darwin.token"} {
		if strings.Contains(errBody, `name="`+secretField+`" value=`) {
			t.Fatalf("secret field %s must never render a value attribute on error re-render: %s", secretField, errBody)
		}
	}
}

// TestConfigUpdates covers GET pre-filling the updates form and a valid POST
// saving changed update fields, scheduling Actions.Apply — the update
// Checker snapshots config at construction, so a change needs a restart to
// take effect.
func TestConfigUpdates(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config/updates", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="update.channel"`) {
		t.Fatalf("GET form: code %d body=%s", rec.Code, rec.Body.String())
	}

	form := baseUpdatesForm()
	form.Set("update.channel", "prerelease")
	form.Set("update.autoApply", "on")
	form.Set("csrf", csrf)
	rec = postForm(t, srv.Handler(), "/config/updates", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST: want 303, got %d: %s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Update.Channel != "prerelease" || !cur.Update.AutoApply || cur.Update.DisableChecks {
		t.Fatalf("Update = %+v, want channel=prerelease autoApply=true checks-on (DisableChecks=false)", cur.Update)
	}

	recGet := getPath(t, srv.Handler(), "/config/updates", cookie)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET after save: want 200, got %d", recGet.Code)
	}
	body := recGet.Body.String()
	if !strings.Contains(body, `value="prerelease" selected`) {
		t.Errorf(`expected prerelease option selected in re-rendered form: %s`, body)
	}
	if !strings.Contains(body, `name="update.autoApply" checked`) {
		t.Errorf("expected update.autoApply checked in re-rendered form: %s", body)
	}
}

// TestConfigUpdatesRestarts is task 7's pinned RED test.
func TestConfigUpdatesRestarts(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := url.Values{"csrf": {csrf}, "update.channel": {"prerelease"}, "update.checks": {"on"}}
	rec := postForm(t, srv.Handler(), "/config/updates", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d", rec.Code)
	}
	awaitApply(t, applyCh) // checker snapshots config at construction: restart required

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Update.Channel != "prerelease" {
		t.Fatalf("update.channel = %q, want prerelease", cur.Update.Channel)
	}
	if cur.Update.DisableChecks {
		t.Fatal("update.checks=on must leave DisableChecks false")
	}
}

// TestConfigUpdatesValidationError covers an out-of-range channel value
// (something a browser's <select> would never submit, but a raw form POST
// can): re-render (200) with the error, file unchanged, Actions.Apply NOT
// called.
func TestConfigUpdatesValidationError(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseUpdatesForm()
	form.Set("update.channel", "nightly-canary")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config/updates", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "update.channel") {
		t.Errorf("expected an update.channel validation message: %s", rec.Body.String())
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged on validation error:\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigAdmin covers GET rendering the admin form and a valid password
// change: 303, the new password verifies, and — unlike every other section —
// Actions.Apply is NOT scheduled (VerifyLogin reads the hash from disk on
// every attempt, so the new password is live immediately without a
// restart). The old session must still be valid afterwards too.
func TestConfigAdmin(t *testing.T) {
	srv, svc, _, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config/admin", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="web.password"`) {
		t.Fatalf("GET form: code %d body=%s", rec.Code, rec.Body.String())
	}

	form := url.Values{"csrf": {csrf}, "web.password": {"newpassword1"}, "web.password.confirm": {"newpassword1"}}
	rec = postForm(t, srv.Handler(), "/config/admin", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST: want 303, got %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case <-applyCh:
		t.Fatal("admin save must NOT restart the board (VerifyLogin reads from disk)")
	case <-time.After(2 * applyDelay):
	}

	if !svc.VerifyLogin("newpassword1") {
		t.Fatal("new password was not stored")
	}
	if svc.VerifyLogin(configTestPassword) {
		t.Fatal("old password must no longer verify")
	}

	// The old session (created before the password change) must still be
	// valid — sessions are independent of the stored password hash.
	recStillIn := getPath(t, srv.Handler(), "/config", cookie)
	if recStillIn.Code != http.StatusOK {
		t.Fatalf("old session should still be valid after an admin password change, got %d", recStillIn.Code)
	}
}

// TestConfigAdminNoRestart is task 7's pinned RED test.
func TestConfigAdminNoRestart(t *testing.T) {
	srv, _, _, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := url.Values{"csrf": {csrf}, "web.password": {"newpassword1"}, "web.password.confirm": {"newpassword1"}}
	rec := postForm(t, srv.Handler(), "/config/admin", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d", rec.Code)
	}
	select {
	case <-applyCh:
		t.Fatal("admin save must NOT restart the board (VerifyLogin reads from disk)")
	case <-time.After(2 * applyDelay):
	}
}

// TestConfigAdminPasswordMismatch covers mismatched web.password/confirm:
// re-render (200) with an error, the stored password unchanged.
func TestConfigAdminPasswordMismatch(t *testing.T) {
	srv, svc, _, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := url.Values{"csrf": {csrf}, "web.password": {"newpassword1"}, "web.password.confirm": {"different1"}}
	rec := postForm(t, srv.Handler(), "/config/admin", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "class=\"error\"") {
		t.Fatalf("expected error markup in body: %s", rec.Body.String())
	}
	if !svc.VerifyLogin(configTestPassword) {
		t.Fatal("stored password must be unchanged on mismatch")
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigAdminShortPassword covers a too-short new password (below
// SetInitialPassword's 8-character floor): re-render (200) with an error,
// the stored password unchanged.
func TestConfigAdminShortPassword(t *testing.T) {
	srv, svc, _, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := url.Values{"csrf": {csrf}, "web.password": {"short"}, "web.password.confirm": {"short"}}
	rec := postForm(t, srv.Handler(), "/config/admin", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "at least 8 characters") {
		t.Fatalf("expected password-length validation error in body: %s", rec.Body.String())
	}
	if !svc.VerifyLogin(configTestPassword) {
		t.Fatal("stored password must be unchanged on a too-short password")
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigAdminSecretsNeverRoundTrip is the reinstated equivalent of the
// old monolith GET-page invariant for the admin page's password inputs:
// placeholder="unchanged", never a value attribute.
func TestConfigAdminSecretsNeverRoundTrip(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config/admin", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="web.password" placeholder="unchanged"`, `name="web.password.confirm" placeholder="unchanged"`, `autocomplete="new-password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in admin page: %s", want, body)
		}
	}
	for _, secretField := range []string{"web.password", "web.password.confirm"} {
		if strings.Contains(body, `name="`+secretField+`" value=`) {
			t.Fatalf("secret field %s must never render a value attribute: %s", secretField, body)
		}
	}
}

// TestOldMonolithConfigPostGone is task 7's pinned RED test: the HTML
// monolith form's save route is retired this task.
func TestOldMonolithConfigPostGone(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := postForm(t, srv.Handler(), "/config", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code == http.StatusOK {
		t.Fatalf("POST /config (HTML monolith) should be gone; got 200")
	}
}

// (b) unauthenticated GET /config redirects to /login.
func TestConfigGetUnauthenticatedRedirects(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	rec := getPath(t, srv.Handler(), "/config")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// The replacements-textarea round trip (POST -> saved -> re-rendered) is
// covered by TestConfigDeparturesReplacementsRoundTrip; the old monolith's
// combined-fieldset POST /config tests (valid-change/update-fields/secrets/
// validation/short-password) are superseded by the per-section
// TestConfigNetwork*/TestConfigUpdates*/TestConfigAdmin* tests above, now
// that POST /config itself is gone (TestOldMonolithConfigPostGone).

// --- parsing helper unit tests ---

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "A", []string{"A"}},
		{"multi trims and drops empties", " A ,, B ,C", []string{"A", "B", "C"}},
		{"all empty", " , , ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitCSV(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitCSV(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestJoinCSV(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"A"}, "A"},
		{"multi", []string{"A", "B", "C"}, "A, B, C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinCSV(tc.in); got != tc.want {
				t.Fatalf("joinCSV(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseReplacements(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    map[string]string
		wantErr bool
	}{
		{"empty", "", map[string]string{}, false},
		{"single line", "Bristol Temple Meads=Bristol TM", map[string]string{"Bristol Temple Meads": "Bristol TM"}, false},
		{"multi line, blank lines ignored", "A=B\n\nC=D\n", map[string]string{"A": "B", "C": "D"}, false},
		{"missing equals rejected", "no-equals-here", nil, true},
		{"empty from rejected", "=value", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReplacements(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseReplacements(%q): want error, got none (result=%#v)", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseReplacements(%q): unexpected error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseReplacements(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatReplacements(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"empty", map[string]string{}, ""},
		{"nil", nil, ""},
		{"single", map[string]string{"A": "B"}, "A=B"},
		{"multiple sorted deterministically", map[string]string{"Z": "1", "A": "2", "M": "3"}, "A=2\nM=3\nZ=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatReplacements(tc.in); got != tc.want {
				t.Fatalf("formatReplacements(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestConfigSectionNav pins the desktop master-detail navigation: every
// config section page renders the shared section nav (confignav, shown as a
// sidebar at desktop widths, display:none on phones) with aria-current="page"
// marking its own link, and the layout stamps the logged-in shell class on
// <body> so the desktop stylesheet can swap the horizontal sign for the
// totem rail without touching logged-out pages.
func TestConfigSectionNav(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	for _, slug := range []string{"departures", "display", "network", "updates", "admin"} {
		body := getPath(t, srv.Handler(), "/config/"+slug, cookie).Body.String()
		if !strings.Contains(body, `class="cfgnav"`) {
			t.Errorf("/config/%s: section nav missing", slug)
		}
		want := `<a href="/config/` + slug + `" class="on" aria-current="page">`
		if !strings.Contains(body, want) {
			t.Errorf("/config/%s: active link %s missing", slug, want)
		}
		if !strings.Contains(body, `<body class="shell`) {
			t.Errorf("/config/%s: logged-in page missing body shell class", slug)
		}
	}

	// Logged-out pages must NOT get the shell class: the desktop rail is a
	// logged-in affordance; login/setup keep the centered column.
	login := getPath(t, srv.Handler(), "/login").Body.String()
	if strings.Contains(login, `<body class="shell`) {
		t.Error("/login: logged-out page must not carry the shell class")
	}
}
