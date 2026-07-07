package config

import (
	"encoding/json"
	"testing"
)

func TestDefaultHasSaneValues(t *testing.T) {
	c := Default()
	if c.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", c.Version, CurrentVersion)
	}
	if c.Board.Services != 3 || c.Board.CutoffHours != 8 || c.Board.RefreshSeconds != 60 {
		t.Errorf("board defaults wrong: %+v", c.Board)
	}
	if c.Board.TimeWindowMinutes != 120 {
		t.Errorf("timeWindow default = %d, want 120", c.Board.TimeWindowMinutes)
	}
	if !c.Layout.Times {
		t.Error("layout.times should default true")
	}
	if c.Powersaving.Enabled {
		t.Error("powersaving should default disabled")
	}
}

func TestConfigRoundTrips(t *testing.T) {
	c := Default()
	c.Darwin.Token = "GUID"
	c.Board.Origin = "PAD"
	c.Board.Replacements = map[string]string{"London ": ""}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Board.Origin != "PAD" || back.Darwin.Token != "GUID" {
		t.Fatalf("round-trip lost data: %+v", back)
	}
	if back.Board.Replacements["London "] != "" {
		t.Fatalf("replacements lost: %+v", back.Board.Replacements)
	}
}

func TestNewSectionsRoundTripJSON(t *testing.T) {
	c := Default()
	c.Web.PasswordHash = "hash"
	c.Wifi = WifiConfig{SSID: "n", PSK: "passphrase"}
	c.Provisioning.APPassword = "appw"
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Web != c.Web || back.Wifi != c.Wifi || back.Provisioning != c.Provisioning {
		t.Fatalf("round trip lost data: %+v", back)
	}
	// Old config documents (no new keys) still load: zero values.
	var old Config
	if err := json.Unmarshal([]byte(`{"version":1}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Web.PasswordHash != "" || old.Wifi.PSK != "" {
		t.Fatal("missing keys must default to zero values")
	}
}

func TestValidateConnectivityTier(t *testing.T) {
	c := Default()
	c.Web.PasswordHash = "$argon2id$fake"
	if err := c.ValidateConnectivity(); err != nil {
		t.Fatalf("password-only config should be connectivity-valid: %v", err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("connectivity-valid config with no origin/token must NOT be board-valid")
	}

	c.Wifi.SSID = "HomeNet" // ssid without psk = incomplete
	if err := c.ValidateConnectivity(); err == nil {
		t.Fatal("SSID without PSK must fail connectivity validation")
	}
	c.Wifi.PSK = "short" // < 8
	if err := c.ValidateConnectivity(); err == nil {
		t.Fatal("PSK under 8 chars must fail")
	}
	c.Wifi.PSK = "longenough"
	if err := c.ValidateConnectivity(); err != nil {
		t.Fatalf("complete wifi should pass: %v", err)
	}

	c.Web.PasswordHash = ""
	if err := c.ValidateConnectivity(); err == nil {
		t.Fatal("no admin password must fail connectivity validation")
	}
}

func TestValidateConnectivityCompatibility(t *testing.T) {
	// When a config has password + board config, both validations should pass.
	c := Default()
	c.Web.PasswordHash = "$argon2id$fake"
	c.Board.Origin = "PAD"
	c.Darwin.Token = "tok"
	if err := c.Validate(); err != nil {
		t.Fatalf("fixture should be board-valid: %v", err)
	}
	if err := c.ValidateConnectivity(); err != nil {
		t.Fatalf("board-valid config should also pass connectivity: %v", err)
	}
}
