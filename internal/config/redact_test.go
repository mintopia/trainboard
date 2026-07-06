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

func TestRedactionCoversNewSecrets(t *testing.T) {
	c := Default()
	c.Darwin.Token = "tok-secret"
	c.Wifi = WifiConfig{SSID: "HomeNet", PSK: "psk-secret"}
	c.Provisioning.APPassword = "ap-secret"
	c.Web.PasswordHash = "$argon2id$fake"
	for name, s := range map[string]string{
		"String":   c.String(),
		"GoString": fmt.Sprintf("%#v", c),
		"v":        fmt.Sprintf("%v", c),
	} {
		for _, secret := range []string{"psk-secret", "ap-secret", "tok-secret", "$argon2id$fake"} {
			if strings.Contains(s, secret) {
				t.Errorf("%s output leaks %q", name, secret)
			}
		}
	}
	r := c.Redacted()
	if r.Wifi.PSK == "psk-secret" || r.Provisioning.APPassword == "ap-secret" {
		t.Fatal("Redacted() must mask wifi.psk and provisioning.apPassword")
	}
	if r.Wifi.SSID != "HomeNet" {
		t.Fatal("SSID is not a secret; must survive redaction")
	}
	empty := Default().Redacted()
	if empty.Wifi.PSK != "" || empty.Provisioning.APPassword != "" {
		t.Fatal("empty secrets must stay empty after redaction")
	}
}
