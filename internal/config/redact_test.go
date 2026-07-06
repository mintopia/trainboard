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

func TestRedactedMasksWebPasswordHash(t *testing.T) {
	c := Default()
	c.Web.PasswordHash = "$argon2id$fake"
	r := c.Redacted()
	if r.Web.PasswordHash == "$argon2id$fake" || r.Web.PasswordHash == "" {
		t.Fatalf("web password hash not masked: %q", r.Web.PasswordHash)
	}
	if c.Web.PasswordHash != "$argon2id$fake" {
		t.Fatal("Redacted mutated the original")
	}
	empty := Default().Redacted()
	if empty.Web.PasswordHash != "" {
		t.Fatal("empty web password hash must stay empty after redaction")
	}
}

func TestBareSubstructStringNeverLeaksSecrets(t *testing.T) {
	wifi := WifiConfig{SSID: "HomeNet", PSK: "psk-secret"}
	provisioning := ProvisioningConfig{APPassword: "ap-secret"}
	web := WebConfig{PasswordHash: "$argon2id$fake"}

	for name, s := range map[string]string{
		"wifi %v":         fmt.Sprintf("%v", wifi),
		"wifi %#v":        fmt.Sprintf("%#v", wifi),
		"provisioning %v": fmt.Sprintf("%v", provisioning),
		"provisioning %#v": fmt.Sprintf(
			"%#v", provisioning,
		),
		"web %v":  fmt.Sprintf("%v", web),
		"web %#v": fmt.Sprintf("%#v", web),
	} {
		for _, secret := range []string{"psk-secret", "ap-secret", "$argon2id$fake"} {
			if strings.Contains(s, secret) {
				t.Errorf("%s output leaks %q: %q", name, secret, s)
			}
		}
	}

	if !strings.Contains(fmt.Sprintf("%v", wifi), "HomeNet") {
		t.Fatal("wifi String() must retain non-secret SSID")
	}
	if !strings.Contains(fmt.Sprintf("%#v", wifi), "HomeNet") {
		t.Fatal("wifi GoString() must retain non-secret SSID")
	}
}
