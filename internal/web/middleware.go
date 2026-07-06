package web

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type middleware = func(http.Handler) http.Handler

// chain wraps h so that mws run in the order given: chain(h, a, b) = a(b(h)).
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ctxKey int

const ctxCSRF ctxKey = iota

// csrfFrom returns the CSRF token requireAuth stored for this request.
func csrfFrom(r *http.Request) string {
	v, _ := r.Context().Value(ctxCSRF).(string)
	return v
}

// requireAuth gates a route on a valid session cookie. HTML routes redirect
// to /login; API routes get 401 JSON. The session's CSRF token is placed in
// the request context for csrfProtect and form rendering.
func requireAuth(s *Sessions, isAPI bool) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(sessionCookie)
			if err == nil {
				if csrf, ok := s.Lookup(c.Value); ok {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxCSRF, csrf)))
					return
				}
			}
			if isAPI {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
		})
	}
}

func stateChanging(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

// csrfProtect enforces the per-session CSRF token on state-changing
// requests. Chain it after requireAuth. Rejections are logged to log (path
// and method only — never the tokens themselves) to satisfy the security
// invariant that CSRF failures are both rejected with 403 and observable.
func csrfProtect(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !stateChanging(r) {
				next.ServeHTTP(w, r)
				return
			}
			want := csrfFrom(r)
			got := r.Header.Get("X-CSRF-Token")
			if got == "" {
				got = r.PostFormValue("csrf")
			}
			if want == "" || got != want {
				log.Warn("csrf rejected", "path", r.URL.Path, "method", r.Method)
				http.Error(w, "csrf token invalid", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originCheck rejects state-changing requests whose Origin header disagrees
// with the request Host. Absent Origin (non-browser clients) is allowed —
// auth and CSRF still gate those.
//
// This middleware runs in the global chain (Handler()), before mux dispatch,
// so it never passes through apiJSONErrors — the per-route middleware that
// otherwise normalises /api/* error bodies to JSON. It therefore has to be
// API-aware itself: /api/* rejections get the uniform {"error":"..."} JSON
// body (mirroring requireAuth's isAPI handling); every other route keeps the
// existing plain-text http.Error body.
func originCheck(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if stateChanging(r) {
				if o := r.Header.Get("Origin"); o != "" {
					u, err := url.Parse(o)
					if err != nil || u.Host == "" || u.Host != r.Host {
						log.Warn("origin rejected", "origin", o, "host", r.Host, "path", r.URL.Path)
						const msg = "cross-origin request rejected"
						if strings.HasPrefix(r.URL.Path, "/api/") {
							writeJSONError(w, http.StatusForbidden, msg)
							return
						}
						http.Error(w, msg, http.StatusForbidden)
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rateLimit applies the limiter to state-changing requests, keyed by client IP.
func rateLimit(rl *limiter, log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if stateChanging(r) {
				ip, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					ip = r.RemoteAddr
				}
				if !rl.allow(ip) {
					log.Warn("rate limited", "ip", ip, "path", r.URL.Path)
					http.Error(w, "too many requests", http.StatusTooManyRequests)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// trackingWriter records whether a response has started (header written or
// body bytes sent) so recoverPanics can decide whether it is still safe to
// write its own 500 response.
type trackingWriter struct {
	http.ResponseWriter
	started bool
}

func (tw *trackingWriter) WriteHeader(code int) {
	tw.started = true
	tw.ResponseWriter.WriteHeader(code)
}

func (tw *trackingWriter) Write(b []byte) (int, error) {
	tw.started = true
	return tw.ResponseWriter.Write(b)
}

// recoverPanics recovers panics from downstream handlers (e.g. Sessions.Create
// failing on crypto/rand exhaustion) so a single bad request cannot take down
// the server. It logs the panic value and, if the response hasn't started
// yet, replies 500. It must be the OUTERMOST middleware in the chain built by
// Task 6, so every other middleware and handler runs under its recover.
func recoverPanics(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tw := &trackingWriter{ResponseWriter: w}
			defer func() {
				if v := recover(); v != nil {
					log.Error("handler panic", "path", r.URL.Path, "panic", fmt.Sprint(v))
					if !tw.started {
						http.Error(tw, "internal error", http.StatusInternalServerError)
					}
				}
			}()
			next.ServeHTTP(tw, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// logRequests emits one line per request. The query string is deliberately
// omitted: it could carry secrets.
func logRequests(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(sw, r)
			log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		})
	}
}
