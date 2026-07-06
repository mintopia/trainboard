package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
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
// requests. Chain it after requireAuth.
func csrfProtect() middleware {
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
func originCheck(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if stateChanging(r) {
				if o := r.Header.Get("Origin"); o != "" {
					u, err := url.Parse(o)
					if err != nil || u.Host == "" || u.Host != r.Host {
						log.Warn("origin rejected", "origin", o, "host", r.Host, "path", r.URL.Path)
						http.Error(w, "cross-origin request rejected", http.StatusForbidden)
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
