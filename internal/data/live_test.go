package data

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveProbe hits the real Darwin Lite endpoint. It is skipped unless
// DARWIN_LITE_API_KEY is set, so CI and offline runs stay green. It is the gate
// that confirms the request envelope/namespaces are actually accepted.
func TestLiveProbe(t *testing.T) {
	token := os.Getenv("DARWIN_LITE_API_KEY")
	if token == "" {
		t.Skip("DARWIN_LITE_API_KEY not set; skipping live Darwin probe")
	}
	crs := os.Getenv("DARWIN_PROBE_CRS")
	if crs == "" {
		crs = "PAD"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	b, err := NewClient(token).Fetch(ctx, Request{OriginCRS: crs, NumRows: 10, TimeWindowMinutes: 120})
	if err != nil {
		t.Fatalf("live probe failed (namespace/token suspect — see soap.go): %v", err)
	}
	if b.CRS == "" || b.LocationName == "" {
		t.Fatalf("live board missing station identity: %+v", b)
	}
	t.Logf("live board: %s (%s), %d departures, %d messages, generatedAt=%s",
		b.LocationName, b.CRS, len(b.Departures), len(b.Messages), b.GeneratedAt)
}
