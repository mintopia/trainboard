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
