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
}

type setupPageData struct {
	basePage
	Error string
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
	s.mux.Handle("POST /config/ap-password", chain(http.HandlerFunc(s.handleConfigAPPassword),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(staticFS())))

	return s
}

// setupGate redirects every request but /setup and /static/ to /setup while
// no admin password is stored; once one exists, /setup itself 404s.
func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.needsSetup() {
			if r.URL.Path != "/setup" {
				http.Redirect(w, r, "/setup", http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/setup" {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler is the outermost handler: recoverPanics wraps everything (per its
// doc comment), then logRequests, then originCheck, then the first-boot
// setup gate, then the route table.
func (s *Server) Handler() http.Handler {
	return chain(s.mux, recoverPanics(s.log), logRequests(s.log), originCheck(s.log), s.setupGate)
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
	case "login":
		t = loginTemplate
	case "index":
		t = statusTemplate
	case "config":
		t = configTemplate
	case "applied":
		t = appliedTemplate
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

func (s *Server) handleSetupGet(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "setup", setupPageData{})
}

func (s *Server) handleSetupPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
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
	http.Redirect(w, r, "/", http.StatusFound)
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
