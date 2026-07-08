package tz

import "testing"

func TestLocationIsEuropeLondon(t *testing.T) {
	loc := Location()
	if loc.String() != "Europe/London" {
		t.Fatalf("Location() = %q, want %q", loc.String(), "Europe/London")
	}
}

func TestLocationIsCached(t *testing.T) {
	first := Location()
	second := Location()
	if first != second {
		t.Fatalf("Location() returned different pointers across calls: %p != %p", first, second)
	}
}
