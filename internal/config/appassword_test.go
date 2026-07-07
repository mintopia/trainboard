package config

import (
	"strings"
	"testing"
)

func TestGenerateAPPasswordLength(t *testing.T) {
	pw, err := GenerateAPPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) != apPasswordLen {
		t.Fatalf("len(pw) = %d, want %d", len(pw), apPasswordLen)
	}
}

// TestGenerateAPPasswordAlphabet draws many passwords and checks every
// character stays within apAlphabet — the visually-ambiguous exclusions
// (0/O, 1/l/I) are the whole point of a dedicated alphabet, so this locks
// in that no other characters ever slip through crypto/rand's mapping.
func TestGenerateAPPasswordAlphabet(t *testing.T) {
	for i := 0; i < 200; i++ {
		pw, err := GenerateAPPassword()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range pw {
			if !strings.ContainsRune(apAlphabet, r) {
				t.Fatalf("password %q contains character %q outside apAlphabet %q", pw, r, apAlphabet)
			}
		}
	}
}

// TestGenerateAPPasswordVaries is a light sanity check that successive
// draws aren't returning a fixed string (would indicate a broken/stubbed
// rand source rather than exercising crypto/rand).
func TestGenerateAPPasswordVaries(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		pw, err := GenerateAPPassword()
		if err != nil {
			t.Fatal(err)
		}
		seen[pw] = true
	}
	if len(seen) < 2 {
		t.Fatalf("20 draws produced only %d distinct password(s)", len(seen))
	}
}
