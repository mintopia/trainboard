// Package config is the versioned local configuration store: a JSON document
// with defaults, validation, transactional writes, and token redaction.
package config

// CurrentVersion is the schema version written by this build.
const CurrentVersion = 1

// Config is the full device configuration document.
type Config struct {
	Version     int              `json:"version"`
	Darwin      DarwinConfig     `json:"darwin"`
	Board       BoardConfig      `json:"board"`
	Layout      LayoutConfig     `json:"layout"`
	Powersaving PowersavingConfig `json:"powersaving"`
}

// DarwinConfig holds the Darwin Lite access token (secret).
type DarwinConfig struct {
	Token string `json:"token"`
}

// BoardConfig holds departure-board content settings.
type BoardConfig struct {
	Origin            string            `json:"origin"`            // CRS
	Destination       string            `json:"destination"`       // optional CRS (server-side filter)
	Platforms         []string          `json:"platforms"`         // client filter
	TOCs              []string          `json:"tocs"`              // client filter (operatorCode)
	// Services is the max rows to display. Plan C maps this to data.Filter.MaxServices
	// (client-side trim) — NOT to data.Request.NumRows, which must stay 10 (the LDBWS
	// WithDetails cap) so server-side capping can't cause a false NoServices.
	Services int `json:"services"` // max rows to show
	CutoffHours       int               `json:"cutoffHours"`       // hide departures beyond this window
	RefreshSeconds    int               `json:"refreshSeconds"`    // poll interval
	TimeWindowMinutes int               `json:"timeWindowMinutes"` // LDBWS timeWindow
	Replacements      map[string]string `json:"replacements"`      // station-name substitutions
}

// LayoutConfig holds display layout toggles.
type LayoutConfig struct {
	Times bool `json:"times"` // show calling-point times
}

// PowersavingConfig dims the panel during a (possibly cross-midnight) window.
type PowersavingConfig struct {
	Enabled    bool   `json:"enabled"`
	Start      string `json:"start"`      // "HH:MM"
	End        string `json:"end"`        // "HH:MM"
	Brightness int    `json:"brightness"` // SSD1322 contrast 0-255 while saving
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
	}
}
