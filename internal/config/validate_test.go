package config

import (
	"strings"
	"testing"
)

func validConfig() Config {
	c := Default()
	c.Darwin.Token = "some-guid"
	c.Board.Origin = "PAD"
	return c
}

func TestValidateAcceptsGoodConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		msg    string
	}{
		{"bad version", func(c *Config) { c.Version = 99 }, "version"},
		{"empty origin", func(c *Config) { c.Board.Origin = "" }, "origin"},
		{"bad origin crs", func(c *Config) { c.Board.Origin = "PADX" }, "origin"},
		{"bad destination crs", func(c *Config) { c.Board.Destination = "rd" }, "destination"},
		{"no token", func(c *Config) { c.Darwin.Token = "" }, "token"},
		{"services too low", func(c *Config) { c.Board.Services = 0 }, "services"},
		{"services too high", func(c *Config) { c.Board.Services = 11 }, "services"},
		{"cutoff negative", func(c *Config) { c.Board.CutoffHours = -1 }, "cutoff"},
		{"refresh too low", func(c *Config) { c.Board.RefreshSeconds = 2 }, "refresh"},
		{"bad powersaving time", func(c *Config) { c.Powersaving.Enabled = true; c.Powersaving.Start = "25:00" }, "powersaving"},
		{"bad powersaving brightness", func(c *Config) { c.Powersaving.Enabled = true; c.Powersaving.Brightness = 300 }, "brightness"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.msg) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.msg)
			}
		})
	}
}

func TestValidateWifiPSKBounds(t *testing.T) {
	c := validConfig()
	c.Wifi.SSID = "HomeNet"
	c.Wifi.PSK = "short7c"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "wifi.psk") {
		t.Fatalf("7-char PSK must fail: %v", err)
	}
	c.Wifi.PSK = strings.Repeat("x", 64)
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "wifi.psk") {
		t.Fatalf("64-char PSK must fail: %v", err)
	}
	c.Wifi.PSK = "goodpassphrase"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid PSK rejected: %v", err)
	}
	c.Wifi.SSID = ""
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "wifi.ssid") {
		t.Fatalf("PSK without SSID must fail: %v", err)
	}
	c.Wifi.SSID = strings.Repeat("s", 33)
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "wifi.ssid") {
		t.Fatalf("33-byte SSID must fail: %v", err)
	}
	c.Wifi = WifiConfig{} // both empty: fine
	if err := c.Validate(); err != nil {
		t.Fatalf("empty wifi rejected: %v", err)
	}
}

func TestValidateWifiCountry(t *testing.T) {
	c := validConfig()

	c.Wifi.Country = "gb" // lowercase
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "wifi.country") {
		t.Fatalf("lowercase country must fail: %v", err)
	}
	c.Wifi.Country = "GBR" // 3 letters
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "wifi.country") {
		t.Fatalf("3-letter country must fail: %v", err)
	}
	c.Wifi.Country = "G1" // digit
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "wifi.country") {
		t.Fatalf("country containing a digit must fail: %v", err)
	}
	c.Wifi.Country = "US"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid 2-letter uppercase country rejected: %v", err)
	}
	c.Wifi.Country = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("empty country (treated as GB downstream) rejected: %v", err)
	}
}
