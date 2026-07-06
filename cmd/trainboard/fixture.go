package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/mintopia/trainboard/internal/data"
)

// fixtureFetcher replays a data.Board from a JSON file: offline dev/demo
// mode. Each Fetch returns a deep-enough copy with a fresh GeneratedAt so
// snapshots stay immutable and the stale grace never trips.
type fixtureFetcher struct {
	raw []byte
}

func newFixtureFetcher(path string) (*fixtureFetcher, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probe data.Board
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	return &fixtureFetcher{raw: raw}, nil
}

func (f *fixtureFetcher) Fetch(_ context.Context, _ data.Request) (*data.Board, error) {
	var b data.Board
	if err := json.Unmarshal(f.raw, &b); err != nil {
		return nil, err
	}
	b.GeneratedAt = time.Now()
	return &b, nil
}
