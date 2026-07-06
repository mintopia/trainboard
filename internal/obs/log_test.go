package obs

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerWritesTextWithoutTime(t *testing.T) {
	var sb strings.Builder
	log := NewLogger(&sb, nil, slog.LevelInfo)
	log.Info("hello", "k", "v")
	out := sb.String()
	if !strings.Contains(out, "msg=hello") || !strings.Contains(out, "k=v") {
		t.Fatalf("missing msg/attr in %q", out)
	}
	if strings.Contains(out, "time=") {
		t.Fatalf("time attr must be dropped for journald, got %q", out)
	}
}

func TestLoggerTeesIntoRing(t *testing.T) {
	ring := NewRing(8)
	log := NewLogger(&strings.Builder{}, ring, slog.LevelInfo)
	log.Info("fetched", "departures", "5")
	log.Warn("fetch failed", "err", "boom")
	events := ring.Events()
	if len(events) != 2 {
		t.Fatalf("ring has %d events, want 2", len(events))
	}
	if events[0].Msg != "fetched" || events[0].Level != slog.LevelInfo {
		t.Fatalf("event 0 = %+v", events[0])
	}
	if events[1].Attrs["err"] != "boom" {
		t.Fatalf("event 1 attrs = %+v", events[1].Attrs)
	}
}

func TestLoggerLevelGate(t *testing.T) {
	ring := NewRing(8)
	var sb strings.Builder
	log := NewLogger(&sb, ring, slog.LevelInfo)
	log.Debug("noisy")
	if ring.Len() != 0 || sb.Len() != 0 {
		t.Fatal("debug record must be dropped at Info level")
	}
}
