package web

import (
	"strings"
	"testing"
)

func TestHashAndVerifyRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %s", h)
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(h, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestHashUniqueSalts(t *testing.T) {
	a, _ := HashPassword("pw")
	b, _ := HashPassword("pw")
	if a == b {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
	if !VerifyPassword(a, "pw") || !VerifyPassword(b, "pw") {
		t.Fatal("both salted hashes must verify")
	}
}

func TestVerifyMalformedNeverPanics(t *testing.T) {
	for _, bad := range []string{"", "$argon2id$", "not-a-hash", "$argon2id$v=19$m=abc,t=2,p=1$AA$BB", "$argon2id$v=19$m=19456,t=2,p=1$!!!$***"} {
		if VerifyPassword(bad, "pw") {
			t.Fatalf("malformed hash %q verified", bad)
		}
	}
}

func TestVerifyRespectsEmbeddedParams(t *testing.T) {
	// A hash produced with different (weaker, fast) parameters must still
	// verify, because parameters come from the PHC string.
	h := mustHashWithParams(t, "pw", 1, 8, 1)
	if !VerifyPassword(h, "pw") {
		t.Fatal("hash with non-default embedded params must verify")
	}
}

func mustHashWithParams(t *testing.T, password string, time, memoryKiB uint32, threads uint8) string {
	t.Helper()
	h, err := hashWithParams(password, time, memoryKiB, threads)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
