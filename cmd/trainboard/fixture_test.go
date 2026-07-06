package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/data"
)

func TestFixtureFetcherLoadsBoardAndFreshensGeneratedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	body := `{"LocationName":"Paddington","CRS":"PAD","GeneratedAt":"2020-01-01T00:00:00Z","Departures":[{"ScheduledTime":"10:32","Status":"On time","Platform":"9","Destination":{"Name":"Bristol Temple Meads"}}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := newFixtureFetcher(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Fetch(context.Background(), data.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if b.LocationName != "Paddington" || len(b.Departures) != 1 {
		t.Fatalf("board = %+v", b)
	}
	if time.Since(b.GeneratedAt) > time.Minute {
		t.Fatal("GeneratedAt must be freshened so the stale grace never trips in fixture mode")
	}
	// Each fetch returns an independent copy (published snapshots are immutable).
	b2, _ := f.Fetch(context.Background(), data.Request{})
	b.Departures[0].Platform = "MUTATED"
	if b2.Departures[0].Platform == "MUTATED" || func() bool { b3, _ := f.Fetch(context.Background(), data.Request{}); return b3.Departures[0].Platform == "MUTATED" }() {
		t.Fatal("fixture fetches must not alias each other")
	}
}

func TestFixtureFetcherMissingFile(t *testing.T) {
	if _, err := newFixtureFetcher("/nonexistent.json"); err == nil {
		t.Fatal("expected error for missing fixture")
	}
}
