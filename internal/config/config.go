// Package config is the versioned local configuration store: a JSON document
// with defaults, validation, transactional writes, and token redaction.
package config

// CurrentVersion is the schema version written by this build.
const CurrentVersion = 1

// Config is the full device configuration document.
type Config struct {
	Version     int               `json:"version"`
	Darwin      DarwinConfig      `json:"darwin"`
	RTT         RTTConfig         `json:"rtt"`
	Board       BoardConfig       `json:"board"`
	Layout      LayoutConfig      `json:"layout"`
	Powersaving PowersavingConfig `json:"powersaving"`
	Web         WebConfig         `json:"web"`
	Wifi        WifiConfig        `json:"wifi"`
	Update      UpdateConfig      `json:"update"`
}

// DarwinConfig holds the Darwin Lite access token (secret).
type DarwinConfig struct {
	Token string `json:"token"`
}

// RTTConfig holds RealTime Trains API credentials (password is secret).
// Both empty (the default) disables headcode enrichment; a missing "rtt"
// key in configs predating this section unmarshals to exactly that.
type RTTConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// BoardConfig holds departure-board content settings.
type BoardConfig struct {
	Origin      string   `json:"origin"`      // CRS
	Destination string   `json:"destination"` // optional CRS (server-side filter)
	Platforms   []string `json:"platforms"`   // client filter
	TOCs        []string `json:"tocs"`        // client filter (operatorCode)
	// Services is the max rows to display. Plan C maps this to data.Filter.MaxServices
	// (client-side trim) — NOT to data.Request.NumRows, which must stay 10 (the LDBWS
	// WithDetails cap) so server-side capping can't cause a false NoServices.
	Services          int               `json:"services"`          // max rows to show
	CutoffHours       int               `json:"cutoffHours"`       // hide departures beyond this window
	RefreshSeconds    int               `json:"refreshSeconds"`    // poll interval
	TimeWindowMinutes int               `json:"timeWindowMinutes"` // LDBWS timeWindow
	Replacements      map[string]string `json:"replacements"`      // station-name substitutions
}

// LayoutConfig holds display layout toggles.
type LayoutConfig struct {
	Times bool `json:"times"` // show calling-point times
	// Headcodes shows the train headcode column (reference layout parity).
	// Off by default — the zero value keeps configs predating this field
	// unchanged, and the column needs RTT credentials to have data anyway.
	Headcodes bool `json:"headcodes"`
}

// PowersavingConfig dims the panel during a (possibly cross-midnight) window.
type PowersavingConfig struct {
	Enabled    bool   `json:"enabled"`
	Start      string `json:"start"`      // "HH:MM"
	End        string `json:"end"`        // "HH:MM"
	Brightness int    `json:"brightness"` // SSD1322 contrast 0-255 while saving
}

// WebConfig holds the admin web UI credential. An empty PasswordHash means
// first-boot setup has not run and /setup is open.
type WebConfig struct {
	PasswordHash string `json:"passwordHash"`
}

// WifiConfig is the desired STA credential set. Stored by M2's UI, applied
// by M3's connectivity manager; inert until then.
type WifiConfig struct {
	SSID string `json:"ssid"`
	PSK  string `json:"psk"`
	// Country is the two-letter regulatory domain (e.g. "GB", "US") passed
	// to `iw reg set` and rendered into both AP drivers' conf templates. An
	// empty value is treated as "GB" by every consumer (Default sets "GB"
	// explicitly; this field is only ever empty for a config predating this
	// field, or a document that has deliberately cleared it).
	Country string `json:"country"`
}

// UpdateConfig controls M5 self-update behaviour. Field polarity is chosen
// so the ZERO VALUE is the desired default for configs written before this
// section existed (a missing JSON key unmarshals to the zero value):
// Channel "" means stable, DisableChecks false means periodic checks run.
// That is why this is DisableChecks and not the spec table's checkEnabled —
// same behaviour, migration-free encoding.
type UpdateConfig struct {
	// Channel is "stable" (or "", its equivalent) or "prerelease".
	Channel string `json:"channel"`
	// AutoApply applies available updates unattended during
	// Config.InUpdateWindow. Off by default: manual apply from the web UI.
	AutoApply bool `json:"autoApply"`
	// DisableChecks turns the periodic GitHub release check off entirely.
	DisableChecks bool `json:"disableChecks"`
}

// EffectiveChannel maps the empty channel to "stable".
func (u UpdateConfig) EffectiveChannel() string {
	if u.Channel == "" {
		return "stable"
	}
	return u.Channel
}

// Default returns a config populated with sane defaults.
func Default() Config {
	return Config{
		Version: CurrentVersion,
		Board: BoardConfig{
			Services:          3,
			CutoffHours:       8,
			RefreshSeconds:    60,
			TimeWindowMinutes: 120,
			Replacements:      map[string]string{},
		},
		Layout: LayoutConfig{Times: true},
		Powersaving: PowersavingConfig{
			Start:      "23:00",
			End:        "07:00",
			Brightness: 32,
		},
		Wifi:   WifiConfig{Country: "GB"},
		Update: UpdateConfig{Channel: "stable"},
	}
}
