package web

import (
	"testing"
	"time"
)

func TestLimiterAllowsBurstThenBlocks(t *testing.T) {
	l := newLimiter(5)
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base }
	for i := 0; i < 5; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d within burst must be allowed", i)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("6th immediate request must be blocked")
	}
	if !l.allow("5.6.7.8") {
		t.Fatal("other client must not be affected")
	}
}

func TestLimiterRefills(t *testing.T) {
	l := newLimiter(60) // 1/sec refill
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base }
	for i := 0; i < 60; i++ {
		l.allow("k")
	}
	if l.allow("k") {
		t.Fatal("bucket must be empty")
	}
	l.now = func() time.Time { return base.Add(2 * time.Second) }
	if !l.allow("k") {
		t.Fatal("2s at 1/sec must refill at least one token")
	}
}
