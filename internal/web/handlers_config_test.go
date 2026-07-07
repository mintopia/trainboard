package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
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

// newConfigTestServer wires a Server over a valid, saved baseline config
// (origin PAD, a known Darwin token, admin password already set) and returns
// the server, its Service, the config file path, and a channel that receives
// a value each time the wired Actions.Apply fires.
func newConfigTestServer(t *testing.T) (srv *Server, svc *Service, path string, applyCh chan struct{}) {
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
		PreviewPNG:    func() []byte { return nil },
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
	svc = NewService(path, src, act, testLog())
	if err := svc.SetInitialPassword(configTestPassword, "PAD", ""); err != nil {
		t.Fatalf("SetInitialPassword: %v", err)
	}
	srv = NewServer(svc, testLog())
	return srv, svc, path, applyCh
}

// baseConfigForm returns a fresh, fully-populated, valid form matching the
// baseline config written by newConfigTestServer (secrets left blank, i.e.
// "keep the stored value"). Callers mutate the returned map per-test; a
// fresh map is built on every call so tests never share state.
func baseConfigForm() url.Values {
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
		"layout.times":            {"on"},
		// powersaving.enabled deliberately absent: config.Default() has it
		// false, and checkbox semantics say an absent key means false.
		"powersaving.start":      {"23:00"},
		"powersaving.end":        {"07:00"},
		"powersaving.brightness": {"32"},
		"wifi.ssid":              {""},
		"wifi.psk":               {""},
		"darwin.token":           {""},
		"web.password":           {""},
		"web.password.confirm":   {""},
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

// (a) GET /config authed -> 200; the token/psk inputs are present but with
// EMPTY value attributes (never pre-filled), and the literal stored token is
// never present anywhere in the body.
func TestConfigGetRendersFormWithoutLeakingSecrets(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	cookie, _ := loginAs(t, srv, configTestPassword)

	rec := getPath(t, srv.Handler(), "/config", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, configTestToken) {
		t.Fatalf("stored Darwin token leaked into config page body: %s", body)
	}
	if !strings.Contains(body, `name="darwin.token"`) {
		t.Fatalf("expected darwin.token input in body: %s", body)
	}
	if !strings.Contains(body, `name="wifi.psk"`) {
		t.Fatalf("expected wifi.psk input in body: %s", body)
	}
	// Neither secret input carries a pre-filled value attribute.
	if strings.Contains(body, `name="darwin.token" value=`) {
		t.Fatalf("darwin.token input must not have a value attribute: %s", body)
	}
	if strings.Contains(body, `name="wifi.psk" value=`) {
		t.Fatalf("wifi.psk input must not have a value attribute: %s", body)
	}
	if !strings.Contains(body, `placeholder="unchanged"`) {
		t.Fatalf("expected 'unchanged' placeholder on secret inputs: %s", body)
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

// (c) POST /config with a valid change (refreshSeconds 90) renders the
// applied page, persists the change to the config file, and fires the fake
// Actions.Apply within ~1s.
func TestConfigPostValidChangeAppliesAndPersists(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := baseConfigForm()
	form.Set("board.refreshSeconds", "90")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 applied page, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Saved") && !strings.Contains(rec.Body.String(), "restarting") {
		t.Fatalf("expected applied-page content, got: %s", rec.Body.String())
	}

	cur, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cur.Board.RefreshSeconds != 90 {
		t.Fatalf("board.refreshSeconds = %d, want 90", cur.Board.RefreshSeconds)
	}

	awaitApply(t, applyCh)
}

// (d) POST /config with blank secret fields keeps the stored Darwin token
// unchanged (write-only: blank means keep).
func TestConfigPostBlankSecretsKeepsStoredToken(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := baseConfigForm()
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cur.Darwin.Token != configTestToken {
		t.Fatalf("Darwin.Token = %q, want unchanged %q", cur.Darwin.Token, configTestToken)
	}
}

// (e) POST /config with a new darwin.token replaces the stored token.
func TestConfigPostNewTokenReplaces(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := baseConfigForm()
	form.Set("darwin.token", "brand-new-token")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cur.Darwin.Token != "brand-new-token" {
		t.Fatalf("Darwin.Token = %q, want replaced value", cur.Darwin.Token)
	}
}

// (f) POST /config with an invalid value (refreshSeconds 5, below the
// minimum of 15) re-renders the form (200) with the validation error text
// visible, the file unchanged, and Actions.Apply NOT called.
func TestConfigPostInvalidRefreshRerendersWithError(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseConfigForm()
	form.Set("board.refreshSeconds", "5")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "refreshSeconds") {
		t.Fatalf("expected refreshSeconds validation error in body: %s", rec.Body.String())
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

// (g) mismatched web.password/confirm re-renders the form with an error and
// leaves the file unchanged.
func TestConfigPostPasswordConfirmMismatch(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseConfigForm()
	form.Set("web.password", "newpassword1")
	form.Set("web.password.confirm", "different1")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "class=\"error\"") {
		t.Fatalf("expected error markup in body: %s", rec.Body.String())
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged on password mismatch:\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// TestConfigPostPartialFailurePreservesOtherFields covers the finding this
// task resolves: parseConfigForm used to bail out on the FIRST unparsable
// field, so every field parsed after it silently reverted to its zero value
// in the re-rendered form. A user who fat-fingers board.services while also
// legitimately changing board.refreshSeconds and powersaving.start must see
// both of those changes preserved in the re-render alongside the services
// error, not wiped back to their old values.
func TestConfigPostPartialFailurePreservesOtherFields(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseConfigForm()
	form.Set("board.services", "abc")
	form.Set("board.refreshSeconds", "120")
	form.Set("powersaving.start", "22:00")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "services") {
		t.Fatalf("expected board.services validation error in body: %s", body)
	}
	if !strings.Contains(body, `value="120"`) {
		t.Fatalf("expected refreshSeconds=120 preserved in re-rendered form: %s", body)
	}
	if !strings.Contains(body, `value="22:00"`) {
		t.Fatalf("expected powersaving.start=22:00 preserved in re-rendered form: %s", body)
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

// TestConfigPostShortPasswordRerendersWithError covers the finding this task
// resolves: UpdateConfig previously enforced no minimum length on a password
// change, unlike SetInitialPassword's 8-character floor. A config POST
// setting web.password to a too-short value must re-render with an error and
// leave the file (and stored password hash) unchanged.
func TestConfigPostShortPasswordRerendersWithError(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	form := baseConfigForm()
	form.Set("web.password", "short")
	form.Set("web.password.confirm", "short")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 re-render, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "at least 8 characters") {
		t.Fatalf("expected password-length validation error in body: %s", rec.Body.String())
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config file must be unchanged on short password:\nbefore=%+v\nafter=%+v", before, after)
	}
	assertApplyNotCalled(t, applyCh)
}

// (h) POST /config/ap-password regenerates the AP password, showing it
// exactly once in the response body, and persists it to the file.
func TestConfigAPPasswordRegenerate(t *testing.T) {
	srv, _, path, _ := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	rec := postForm(t, srv.Handler(), "/config/ap-password", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur.Provisioning.APPassword) != 12 {
		t.Fatalf("stored AP password length = %d, want 12: %q", len(cur.Provisioning.APPassword), cur.Provisioning.APPassword)
	}
	count := strings.Count(body, cur.Provisioning.APPassword)
	if count != 1 {
		t.Fatalf("expected the new AP password exactly once in body, got %d occurrences: %s", count, body)
	}
}

// (i) unauthenticated POST /config/ap-password redirects to /login (no
// password minted, no CSRF needed to observe the redirect).
func TestConfigAPPasswordUnauthenticatedRedirects(t *testing.T) {
	srv, _, _, _ := newConfigTestServer(t)
	rec := postForm(t, srv.Handler(), "/config/ap-password", url.Values{})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("want 302 /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// (j) the replacements textarea round-trips: "Bristol Temple Meads=Bristol
// TM" submitted, saved, reloaded from the redacted config, and re-rendered
// in the textarea in the same "from=to" form.
func TestConfigReplacementsRoundTrip(t *testing.T) {
	srv, _, path, applyCh := newConfigTestServer(t)
	cookie, csrf := loginAs(t, srv, configTestPassword)

	form := baseConfigForm()
	form.Set("board.replacements", "Bristol Temple Meads=Bristol TM")
	form.Set("csrf", csrf)
	rec := postForm(t, srv.Handler(), "/config", form, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)

	cur, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cur.Board.Replacements["Bristol Temple Meads"]; got != "Bristol TM" {
		t.Fatalf("Replacements[Bristol Temple Meads] = %q, want %q", got, "Bristol TM")
	}

	rec2 := getPath(t, srv.Handler(), "/config", cookie)
	if rec2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "Bristol Temple Meads=Bristol TM") {
		t.Fatalf("expected replacements textarea to round-trip, got: %s", rec2.Body.String())
	}
}

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
