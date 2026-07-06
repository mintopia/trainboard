package web

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// SessionTTL is how long a login lasts; the store is in-memory, so a device
// restart logs everyone out (acceptable for a single-admin appliance).
const SessionTTL = 7 * 24 * time.Hour

//nolint:unused // consumed by the session-cookie middleware landing in a later M2 task
const sessionCookie = "tb_session"

type sessionEntry struct {
	csrf    string
	expires time.Time
}

// Sessions is a thread-safe in-memory session store. Each session carries
// its own CSRF token.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]sessionEntry
	now      func() time.Time
}

// NewSessions returns an empty store.
func NewSessions() *Sessions {
	return &Sessions{sessions: map[string]sessionEntry{}, now: time.Now}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("web: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Create mints a new session and returns its cookie token and CSRF token.
func (s *Sessions) Create() (token, csrf string) {
	token, csrf = randomToken(), randomToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[token] = sessionEntry{csrf: csrf, expires: s.now().Add(SessionTTL)}
	return token, csrf
}

// Lookup returns the session's CSRF token if the session exists and has not
// expired; expired entries are removed.
func (s *Sessions) Lookup(token string) (csrf string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.sessions[token]
	if !found {
		return "", false
	}
	if s.now().After(e.expires) {
		delete(s.sessions, token)
		return "", false
	}
	return e.csrf, true
}

// Destroy removes a session (logout).
func (s *Sessions) Destroy(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}
