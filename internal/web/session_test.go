package web

import (
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	s := NewSessions()
	tok, csrf := s.Create()
	if tok == "" || csrf == "" || tok == csrf {
		t.Fatalf("bad tokens: %q %q", tok, csrf)
	}
	got, ok := s.Lookup(tok)
	if !ok || got != csrf {
		t.Fatal("fresh session must look up with its csrf token")
	}
	s.Destroy(tok)
	if _, ok := s.Lookup(tok); ok {
		t.Fatal("destroyed session must not look up")
	}
}

func TestSessionExpiry(t *testing.T) {
	s := NewSessions()
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	tok, _ := s.Create()
	s.now = func() time.Time { return base.Add(SessionTTL - time.Minute) }
	if _, ok := s.Lookup(tok); !ok {
		t.Fatal("session must be valid inside TTL")
	}
	s.now = func() time.Time { return base.Add(SessionTTL + time.Minute) }
	if _, ok := s.Lookup(tok); ok {
		t.Fatal("session must expire after TTL")
	}
	if _, ok := s.Lookup(tok); ok {
		t.Fatal("expired session must stay gone")
	}
}

func TestSessionTokensUnique(t *testing.T) {
	s := NewSessions()
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, _ := s.Create()
		if seen[tok] {
			t.Fatal("duplicate session token")
		}
		seen[tok] = true
	}
}

func TestSessionConcurrentAccess(_ *testing.T) {
	s := NewSessions()
	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 200; i++ {
				tok, _ := s.Create()
				s.Lookup(tok)
				s.Destroy(tok)
			}
		}()
	}
	for w := 0; w < 8; w++ {
		<-done
	}
}
