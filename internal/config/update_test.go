package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUpdateConfigZeroValueDefaults(t *testing.T) {
	// A pre-M5 config document has no "update" key at all; its zero value
	// must mean: stable channel, no auto-apply, checks enabled.
	var c Config
	if err := json.Unmarshal([]byte(`{"version":1}`), &c); err != nil {
		t.Fatal(err)
	}
	if got := c.Update.EffectiveChannel(); got != "stable" {
		t.Errorf("EffectiveChannel = %q, want stable", got)
	}
	if c.Update.AutoApply || c.Update.DisableChecks {
		t.Errorf("zero value: AutoApply=%v DisableChecks=%v, want false/false",
			c.Update.AutoApply, c.Update.DisableChecks)
	}
}

func TestUpdateChannelValidation(t *testing.T) {
	base := Default()
	base.Board.Origin = "KGX"
	base.Darwin.Token = "tok"
	for _, ch := range []string{"", "stable", "prerelease"} {
		c := base
		c.Update.Channel = ch
		if err := c.Validate(); err != nil {
			t.Errorf("channel %q rejected: %v", ch, err)
		}
	}
	c := base
	c.Update.Channel = "nightly"
	if err := c.Validate(); err == nil {
		t.Error("channel \"nightly\" accepted")
	}
}

func TestInUpdateWindow(t *testing.T) {
	// Times below are Europe/London wall-clock (project TZ rule).
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	at := func(hhmm string) time.Time {
		tt, err := time.ParseInLocation("2006-01-02 15:04", "2026-07-09 "+hhmm, london)
		if err != nil {
			t.Fatal(err)
		}
		return tt
	}

	var c Config // powersaving disabled ⇒ fallback window 03:00–05:00
	if !c.InUpdateWindow(at("04:00")) {
		t.Error("04:00 not in default window")
	}
	if c.InUpdateWindow(at("12:00")) || c.InUpdateWindow(at("05:00")) {
		t.Error("12:00 or 05:00 wrongly inside default window (end is exclusive)")
	}

	c.Powersaving = PowersavingConfig{Enabled: true, Start: "23:00", End: "07:00", Brightness: 32}
	if !c.InUpdateWindow(at("23:30")) || !c.InUpdateWindow(at("06:00")) {
		t.Error("cross-midnight powersave window not honoured")
	}
	if c.InUpdateWindow(at("12:00")) {
		t.Error("midday wrongly inside powersave window")
	}
}
