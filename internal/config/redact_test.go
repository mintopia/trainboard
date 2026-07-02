package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestRedactedMasksToken(t *testing.T) {
	c := Default()
	c.Darwin.Token = "super-secret-guid"
	c.Board.Origin = "PAD"
	r := c.Redacted()
	if r.Darwin.Token == "super-secret-guid" || r.Darwin.Token == "" {
		t.Fatalf("token not masked: %q", r.Darwin.Token)
	}
	if c.Darwin.Token != "super-secret-guid" {
		t.Fatal("Redacted mutated the original")
	}
}

func TestStringNeverLeaksToken(t *testing.T) {
	c := Default()
	c.Darwin.Token = "super-secret-guid"
	c.Board.Origin = "PAD"
	for _, s := range []string{
		c.String(),
		fmt.Sprintf("%v", c),
		//nolint:staticcheck // test that %s and %v both use Stringer interface
		fmt.Sprintf("%s", c),
		fmt.Sprintf("%v", c.Darwin),
	} {
		if strings.Contains(s, "super-secret-guid") {
			t.Fatalf("token leaked in %q", s)
		}
	}
}

func TestRedactedEmptyTokenStaysEmpty(t *testing.T) {
	c := Default()
	if c.Redacted().Darwin.Token != "" {
		t.Fatal("empty token should stay empty when redacted")
	}
}

func TestGoStringNeverLeaksToken(t *testing.T) {
	c := Default()
	c.Darwin.Token = "super-secret-guid"
	c.Board.Origin = "PAD"
	for _, s := range []string{
		fmt.Sprintf("%#v", c),
		fmt.Sprintf("%#v", c.Darwin),
	} {
		if strings.Contains(s, "super-secret-guid") {
			t.Fatalf("token leaked via %%#v in %q", s)
		}
	}
}
