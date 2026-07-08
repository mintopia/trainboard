package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

// apHotspot is the fixed AP-mode hotspot fixture used throughout this file —
// its contents don't matter to the probe handlers or setupGate, only that
// Hotspot() returns non-nil.
var apHotspot = &board.Hotspot{SSID: "Trainboard-AB12", Addr: "192.168.4.1"}

// newVirginAPTestServer wires a Server to a virgin device (no admin password
// yet, i.e. needsSetup() true) whose Sources.Hotspot reports AP mode active
// — the state a phone associates to during initial provisioning, before a
// human has ever visited /setup.
func newVirginAPTestServer(t *testing.T) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, validCfg()); err != nil {
		t.Fatal(err)
	}
	src := Sources{
		Snapshot:   func() *board.Snapshot { return nil },
		Ring:       obs.NewRing(8),
		PreviewPNG: func() []byte { return nil },
		Version:    "vtest",
		StartedAt:  time.Now(),
		Hotspot:    func() *board.Hotspot { return apHotspot },
	}
	act := Actions{Apply: func() {}, Reboot: func() error { return nil }}
	svc := NewService(path, src, act, testLog())
	return NewServer(svc, testLog())
}

// TestSetupGateAbsoluteRedirectInAPMode pins the AP-mode variant of
// setupGate's blanket "no password yet" redirect: a captive-portal-following
// phone carries Host: connectivitycheck.gstatic.com (the host it was
// probing), so a relative "/setup" Location would resolve against the wrong
// host entirely. In AP mode the target must be the absolute setup URL
// instead, matching the on-screen address the CNA/browser will actually load.
func TestSetupGateAbsoluteRedirectInAPMode(t *testing.T) {
	srv := newVirginAPTestServer(t)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "connectivitycheck.gstatic.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != apSetupURL {
		t.Fatalf("want 302 %s, got %d %q", apSetupURL, rec.Code, rec.Header().Get("Location"))
	}
}

// probeCase is one row of the captive-portal probe matrix: a single probe
// path, checked once with the board in AP mode and once with it not — the
// "matrix rows" the task-3 brief calls for alongside
// TestRouteSecurityInvariantMatrix's session-gated rows, except these two
// arms are AP-mode/not-AP-mode rather than no-session/valid-session, because
// these three routes are never session-gated at all (see the extended
// comment on TestRouteSecurityInvariantMatrix in e2e_test.go).
type probeCase struct {
	name           string
	path           string
	wantAPStatus   int
	wantAPLoc      string   // "" = don't check
	wantAPContains []string // body substrings required in AP mode
	wantAPContType string   // Content-Type prefix expected in AP mode, "" = don't check
}

func TestPortalProbeMatrix(t *testing.T) {
	cases := []probeCase{
		{
			name:         "generate_204",
			path:         "/generate_204",
			wantAPStatus: http.StatusFound,
			wantAPLoc:    apSetupURL,
		},
		{
			name:           "hotspot-detect.html",
			path:           "/hotspot-detect.html",
			wantAPStatus:   http.StatusOK,
			wantAPContType: "text/html",
			wantAPContains: []string{
				`<meta http-equiv="refresh" content="0;url=` + apSetupURL + `">`,
				`Redirecting to setup`,
				`<a href="` + apSetupURL + `">setup</a>`,
			},
		},
		{
			name:         "ncsi.txt",
			path:         "/ncsi.txt",
			wantAPStatus: http.StatusFound,
			wantAPLoc:    apSetupURL,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _, _, conn := newConnTestServer(t)
			h := srv.Handler()

			// Not AP mode: 404, regardless of which OS is asking.
			conn.set(nil, "")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("not AP: want 404, got %d body=%s", rec.Code, rec.Body.String())
			}

			// AP mode: the documented per-probe response, reachable with no
			// session and no CSRF token at all (pre-auth, pre-CSRF by design).
			conn.set(apHotspot, "")
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.wantAPStatus {
				t.Fatalf("AP mode: want status %d, got %d body=%s", tc.wantAPStatus, rec.Code, rec.Body.String())
			}
			if tc.wantAPLoc != "" {
				if loc := rec.Header().Get("Location"); loc != tc.wantAPLoc {
					t.Fatalf("AP mode: want Location %q, got %q", tc.wantAPLoc, loc)
				}
			}
			if tc.wantAPContType != "" {
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantAPContType) {
					t.Fatalf("AP mode: want Content-Type prefix %q, got %q", tc.wantAPContType, ct)
				}
			}
			for _, want := range tc.wantAPContains {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("AP mode body missing %q: %s", want, rec.Body.String())
				}
			}
		})
	}
}
