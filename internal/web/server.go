package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Server is the embedded admin UI: templates, session auth, and the route
// table built by NewServer (this task) and extended by later tasks.
type Server struct {
	svc         *Service
	sessions    *Sessions
	mux         *http.ServeMux
	log         *slog.Logger
	authLimit   *limiter
	actionLimit *limiter
	needsSetup  func() bool
}

// basePage carries the fields every rendered page needs: whether the layout
// shows the logged-in nav, and that session's CSRF token for its logout
// form.
type basePage struct {
	LoggedIn bool
	CSRF     string
	Active   string // which tab: "status" | "config" | "actions" | ""
}

type setupPageData struct {
	basePage
	Error string
	// APMode selects setup.html's AP-mode content block (WiFi SSID/PSK +
	// admin password, no origin/token) over the LAN-mode three-field form.
	// Set from Service.Hotspot() != nil.
	APMode bool
	// LastError surfaces the most recent failed WiFi join (Service.LastSTAError)
	// to a phone reconnecting to the hotspot after a bad attempt. Only
	// rendered in the AP-mode block; always "" in LAN mode.
	LastError string
}

// setupWifiStatusPageData is setup_wifi_status.html's render data: the
// pre-auth, read-only status view served on GET /setup once a device is
// provisioned but still in AP fallback (Hotspot() != nil). It deliberately
// carries ONLY the last join error and the configured SSID — no secrets, no
// other config, no runtime status — because /setup has no session behind it.
type setupWifiStatusPageData struct {
	basePage
	// LastError is the most recent failed WiFi-join error (Service.LastSTAError),
	// or "" when none is recorded yet (the "still trying" copy path).
	LastError string
	// SSID is the configured WiFi network name (Wifi.SSID), read via the
	// existing ConfigRedacted path; it is not a secret. "" when unavailable.
	SSID string
}

// setupWifiDonePageData is setup_wifi_done.html's render data: the
// credential-handoff page shown after a successful AP-mode partial setup.
type setupWifiDonePageData struct {
	basePage
	// SSID is the network the board is about to attempt, named in the
	// handoff copy so the user knows what to reconnect to if it fails.
	SSID string
}

type loginPageData struct {
	basePage
	Error string
}

// NewServer builds the full route table: /setup, /login, /logout, /static/,
// the authed status page (/), its live preview image (/preview.png), its
// polled event feed (/events), and later tasks add the rest. authLimit gates
// setup/login POSTs at a burst of 5/min; actionLimit gates other
// state-changing routes at 30/min.
func NewServer(svc *Service, log *slog.Logger) *Server {
	s := &Server{
		svc:         svc,
		sessions:    NewSessions(),
		mux:         http.NewServeMux(),
		log:         log,
		authLimit:   newLimiter(5),
		actionLimit: newLimiter(30),
		needsSetup:  svc.NeedsSetup,
	}

	s.mux.HandleFunc("GET /setup", s.handleSetupGet)
	s.mux.Handle("POST /setup", chain(http.HandlerFunc(s.handleSetupPost), rateLimit(s.authLimit, log)))
	s.mux.HandleFunc("GET /login", s.handleLoginGet)
	s.mux.Handle("POST /login", chain(http.HandlerFunc(s.handleLoginPost), rateLimit(s.authLimit, log)))
	s.mux.Handle("POST /logout", chain(http.HandlerFunc(s.handleLogout),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("GET /", chain(http.HandlerFunc(s.handleIndex), requireAuth(s.sessions, false)))
	s.mux.Handle("GET /preview.png", chain(http.HandlerFunc(s.handlePreviewPNG), requireAuth(s.sessions, false)))
	s.mux.Handle("GET /events", chain(http.HandlerFunc(s.handleEvents), requireAuth(s.sessions, false)))
	s.mux.Handle("GET /config", chain(http.HandlerFunc(s.handleConfigGet), requireAuth(s.sessions, false)))
	s.mux.Handle("POST /config", chain(http.HandlerFunc(s.handleConfigPost),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(staticFS())))

	// Captive-portal probe endpoints (handlers_portal.go): NO auth, NO CSRF
	// by design — see isPortalProbePath's doc comment and setupGate below.
	s.mux.HandleFunc("GET /generate_204", s.handleGenerate204)
	s.mux.HandleFunc("GET /hotspot-detect.html", s.handleHotspotDetect)
	s.mux.HandleFunc("GET /ncsi.txt", s.handleNCSI)

	s.mux.Handle("GET /actions", chain(http.HandlerFunc(s.handleActionsGet), requireAuth(s.sessions, false)))
	s.mux.Handle("POST /actions/restart", chain(http.HandlerFunc(s.handleActionsRestart),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/reboot", chain(http.HandlerFunc(s.handleActionsReboot),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/soak", chain(http.HandlerFunc(s.handleActionsSoak),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/soak/cancel", chain(http.HandlerFunc(s.handleActionsSoakCancel),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/wifi-retry", chain(http.HandlerFunc(s.handleActionsWifiRetry),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/update/check", chain(http.HandlerFunc(s.handleUpdateCheck),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/update/apply", chain(http.HandlerFunc(s.handleUpdateApply),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/update/dismiss", chain(http.HandlerFunc(s.handleUpdateDismiss),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))

	// JSON API: mirrors the HTML surface. requireAuth(s.sessions, true) gives
	// 401 JSON instead of a redirect; apiJSONErrors is outermost so it can
	// also translate the shared csrfProtect/rateLimit middleware's plain-text
	// 403/429 into the API's uniform {"error":"..."} shape.
	s.mux.Handle("GET /api/status", chain(http.HandlerFunc(s.handleAPIStatus),
		apiJSONErrors, requireAuth(s.sessions, true)))
	s.mux.Handle("GET /api/config", chain(http.HandlerFunc(s.handleAPIConfigGet),
		apiJSONErrors, requireAuth(s.sessions, true)))
	s.mux.Handle("PUT /api/config", chain(http.HandlerFunc(s.handleAPIConfigPut),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
	s.mux.Handle("GET /api/events", chain(http.HandlerFunc(s.handleAPIEvents),
		apiJSONErrors, requireAuth(s.sessions, true)))
	s.mux.Handle("POST /api/actions/restart", chain(http.HandlerFunc(s.handleAPIActionsRestart),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
	s.mux.Handle("POST /api/actions/reboot", chain(http.HandlerFunc(s.handleAPIActionsReboot),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
	s.mux.Handle("POST /api/actions/soak", chain(http.HandlerFunc(s.handleAPIActionsSoak),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
	s.mux.Handle("POST /api/actions/soak/cancel", chain(http.HandlerFunc(s.handleAPIActionsSoakCancel),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
	s.mux.Handle("POST /api/actions/wifi-retry", chain(http.HandlerFunc(s.handleAPIActionsWifiRetry),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
	s.mux.Handle("POST /api/actions/update/check", chain(http.HandlerFunc(s.handleAPIUpdateCheck),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
	s.mux.Handle("POST /api/actions/update/apply", chain(http.HandlerFunc(s.handleAPIUpdateApply),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))

	return s
}

// setupGate redirects every request but /setup, /static/, and the
// captive-portal probe endpoints to /setup while no admin password is
// stored; once one exists, /setup itself 404s. The probe endpoints are
// exempted like /static/ so they always reach their own AP-mode-aware
// handlers instead of setupGate's generic redirect, which cannot answer the
// OS-specific bodies those probes expect (see handlers_portal.go).
//
// While a password is still needed, the redirect target is normally the
// relative "/setup" — but in AP mode (svc.Hotspot() != nil) a
// probe-following phone arrives with an unrelated Host header (e.g.
// connectivitycheck.gstatic.com), against which a relative Location would
// resolve to the wrong origin entirely. In that case the target is instead
// the absolute apSetupURL, matching the address the CNA/browser actually
// displays.
func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") || isPortalProbePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if s.needsSetup() {
			if r.URL.Path != "/setup" {
				loc := "/setup"
				if s.svc.Hotspot() != nil {
					loc = apSetupURL
				}
				http.Redirect(w, r, loc, http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/setup" {
			// Provisioned device (a password exists). In AP fallback
			// (Hotspot() != nil) a phone that reconnected to the hotspot after
			// a failed WiFi join still needs the join error, which is only ever
			// rendered on /setup — so GET serves the read-only status view (see
			// handleSetupGet). POST /setup stays refused, and in LAN mode
			// (Hotspot()==nil) /setup 404s for every method exactly as before.
			if r.Method == http.MethodGet && s.svc.Hotspot() != nil {
				next.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler is the outermost handler: recoverPanics wraps everything (per its
// doc comment), then logRequests, then noteProvisioning (every request
// counts as provisioning activity if it comes from the AP subnet), then
// originCheck, then the first-boot setup gate, then the route table.
func (s *Server) Handler() http.Handler {
	return chain(s.mux, recoverPanics(s.log), logRequests(s.log), noteProvisioning(s.svc), originCheck(s.log), s.setupGate)
}

// Run serves Handler() on addr until ctx is cancelled, then shuts down
// gracefully within a 5s budget. It returns nil on a clean shutdown.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("web: graceful shutdown: %w", err)
		}
		return nil
	}
}

// render executes page's template (layout + that page's content/title
// blocks) into w.
func (s *Server) render(w http.ResponseWriter, page string, data any) {
	var t *template.Template
	switch page {
	case "setup":
		t = setupTemplate
	case "setupDone":
		t = setupDoneTemplate
	case "setupWifiDone":
		t = setupWifiDoneTemplate
	case "setupWifiStatus":
		t = setupWifiStatusTemplate
	case "login":
		t = loginTemplate
	case "index":
		t = statusTemplate
	case "config":
		t = configTemplate
	case "applied":
		t = appliedTemplate
	case "actions":
		t = actionsTemplate
	case "rebooting":
		t = rebootingTemplate
	default:
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		s.log.Error("template render failed", "page", page, "error", err.Error())
	}
}

// setSessionCookie sets a hardened session cookie: HttpOnly, SameSite=Strict,
// scoped to the whole site, expiring with the session TTL.
func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(SessionTTL.Seconds()),
	})
}

// expireSessionCookie clears the session cookie client-side (logout).
func expireSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// handleSetupGet renders /setup: the LAN-mode three-field form (password,
// origin, Darwin token) normally, or — while the connectivity manager
// reports AP mode (Service.Hotspot() != nil) — the partial WiFi+password
// form, with any previously-recorded STA join error surfaced for a phone
// reconnecting to the hotspot after a failed attempt.
func (s *Server) handleSetupGet(w http.ResponseWriter, _ *http.Request) {
	// A provisioned device still in AP fallback: the setup form is gone
	// (setupGate 404s POST /setup once a password exists), but a phone
	// reconnecting to the hotspot after a failed WiFi join needs the join
	// error, which lives only here. Serve the read-only status view — no
	// form, no config beyond the (non-secret) SSID, no secrets, no status
	// internals — since this route is pre-auth.
	if s.svc.Hotspot() != nil && !s.needsSetup() {
		s.render(w, "setupWifiStatus", setupWifiStatusPageData{
			LastError: s.svc.LastSTAError(),
			SSID:      s.configuredSSID(),
		})
		return
	}
	s.render(w, "setup", setupPageData{
		APMode:    s.svc.Hotspot() != nil,
		LastError: s.svc.LastSTAError(),
	})
}

// configuredSSID returns the stored WiFi SSID via the existing
// Service.ConfigRedacted read path (SSID is not a secret; Redacted masks only
// the PSK). It returns "" on any load error rather than surfacing it: the
// read-only status view names the network only as a convenience.
func (s *Server) configuredSSID() string {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		return ""
	}
	return cfg.Wifi.SSID
}

// handleSetupPost validates and stores the submitted first-boot config on
// success. It branches on AP mode (Service.Hotspot() != nil) at submit time,
// matching whichever form handleSetupGet actually rendered:
//
//   - LAN mode (unchanged from before this AP-mode flow existed): admin
//     password + origin + Darwin token. Unlike a redirect-to-/, a success does
//     NOT hand the browser straight into an authed "/" — this device was,
//     until this call, running runConfigErrorLoop's static E04 snapshot with
//     no poller at all, so something must actually restart the process for
//     the newly-valid config to take effect and E04 to clear. That is exactly
//     what handleConfigPost's scheduleApply() does for later config saves, so
//     setup schedules the same apply-by-restart here and renders a "setup
//     done, restarting" page instead of redirecting — mirroring
//     handleConfigPost/handleActionsRestart's render-then-scheduleApply shape
//     rather than diverging from it. The session cookie is still issued
//     (harmless: it is an in-memory session that dies with the process either
//     way), so a browser that reloads before the restart completes is still
//     authed, and one that reloads after it lands on /login per setupGate.
//
//   - AP mode: admin password + WiFi SSID/PSK only (see
//     handleSetupPostAPMode) — no origin/token collected here, and
//     deliberately NO scheduleApply/restart: the connectivity manager already
//     re-reads STA credentials from disk on every join attempt (Task 4), so
//     Service.WifiRetryNow alone is enough to make the new creds live.
func (s *Server) handleSetupPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if s.svc.Hotspot() != nil {
		s.handleSetupPostAPMode(w, r)
		return
	}

	pw := r.PostFormValue("password")
	confirm := r.PostFormValue("confirm")
	origin := strings.ToUpper(strings.TrimSpace(r.PostFormValue("origin")))
	token := r.PostFormValue("token")

	if pw != confirm {
		s.render(w, "setup", setupPageData{Error: "passwords do not match"})
		return
	}
	if err := s.svc.SetInitialPassword(pw, origin, token); err != nil {
		s.render(w, "setup", setupPageData{Error: err.Error()})
		return
	}

	tok, _ := s.sessions.Create()
	setSessionCookie(w, tok)
	s.render(w, "setupDone", basePage{LoggedIn: false, CSRF: csrfFrom(r)})
	s.scheduleApply()
}

// handleSetupPostAPMode handles the AP-mode partial setup: admin password +
// WiFi SSID/PSK, via Service.SetupConnectivity. On success it renders the
// credential-handoff page (setup_wifi_done.html) — telling the user the
// hotspot is about to drop — and only THEN, via scheduleWifiRetry (the same
// render-then-time.AfterFunc shape as scheduleApply), calls
// Service.WifiRetryNow, so the response reaches the phone before the AP
// actually tears down. No session cookie is issued here: the browser is
// about to lose its connection to this device's hotspot entirely, so an
// AP-mode session cookie would just be dead weight; the LAN-side login that
// finishes provisioning at /config happens fresh once the board rejoins the
// network.
func (s *Server) handleSetupPostAPMode(w http.ResponseWriter, r *http.Request) {
	pw := r.PostFormValue("password")
	confirm := r.PostFormValue("confirm")
	ssid := strings.TrimSpace(r.PostFormValue("ssid"))
	psk := r.PostFormValue("psk")

	if pw != confirm {
		s.render(w, "setup", setupPageData{APMode: true, LastError: s.svc.LastSTAError(), Error: "passwords do not match"})
		return
	}
	if err := s.svc.SetupConnectivity(pw, ssid, psk); err != nil {
		s.render(w, "setup", setupPageData{APMode: true, LastError: s.svc.LastSTAError(), Error: err.Error()})
		return
	}

	s.render(w, "setupWifiDone", setupWifiDonePageData{basePage: basePage{CSRF: csrfFrom(r)}, SSID: ssid})
	s.scheduleWifiRetry()
}

func (s *Server) handleLoginGet(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "login", loginPageData{})
}

func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	pw := r.PostFormValue("password")
	if !s.svc.VerifyLogin(pw) {
		s.render(w, "login", loginPageData{Error: "incorrect password"})
		return
	}

	tok, _ := s.sessions.Create()
	setSessionCookie(w, tok)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.Destroy(c.Value)
	}
	expireSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}
