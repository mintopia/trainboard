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
