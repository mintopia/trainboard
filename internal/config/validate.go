package config

import (
	"fmt"
	"time"
)

// Validate checks the config is internally consistent and usable. A non-nil
// error means the runtime should fall back to provisioning (AP mode).
func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("config: unsupported version %d (want %d)", c.Version, CurrentVersion)
	}
	if !isCRS(c.Board.Origin) {
		return fmt.Errorf("config: board.origin %q is not a 3-letter CRS code", c.Board.Origin)
	}
	if c.Board.Destination != "" && !isCRS(c.Board.Destination) {
		return fmt.Errorf("config: board.destination %q is not a 3-letter CRS code", c.Board.Destination)
	}
	if c.Darwin.Token == "" {
		return fmt.Errorf("config: darwin.token is required")
	}
	if c.Board.Services < 1 || c.Board.Services > 10 {
		return fmt.Errorf("config: board.services %d out of range 1-10", c.Board.Services)
	}
	if c.Board.CutoffHours < 0 {
		return fmt.Errorf("config: board.cutoffHours %d must be >= 0", c.Board.CutoffHours)
	}
	if c.Board.RefreshSeconds < 15 {
		return fmt.Errorf("config: board.refreshSeconds %d too low (min 15)", c.Board.RefreshSeconds)
	}
	if c.Board.TimeWindowMinutes < 1 {
		return fmt.Errorf("config: board.timeWindowMinutes %d must be >= 1", c.Board.TimeWindowMinutes)
	}
	if c.Powersaving.Enabled {
		if !isHHMM(c.Powersaving.Start) || !isHHMM(c.Powersaving.End) {
			return fmt.Errorf("config: powersaving start/end must be HH:MM (got %q/%q)", c.Powersaving.Start, c.Powersaving.End)
		}
		if c.Powersaving.Brightness < 0 || c.Powersaving.Brightness > 255 {
			return fmt.Errorf("config: powersaving.brightness %d out of range 0-255", c.Powersaving.Brightness)
		}
	}
	if c.Wifi.PSK != "" {
		if n := len(c.Wifi.PSK); n < 8 || n > 63 {
			return fmt.Errorf("config: wifi.psk must be 8-63 characters, got %d", n)
		}
		if c.Wifi.SSID == "" {
			return fmt.Errorf("config: wifi.ssid is required when wifi.psk is set")
		}
	}
	if len(c.Wifi.SSID) > 32 {
		return fmt.Errorf("config: wifi.ssid exceeds 32 bytes")
	}
	return nil
}

// isCRS reports whether s is a 3-letter uppercase CRS code.
func isCRS(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// isHHMM reports whether s parses as a 24h "HH:MM" time.
func isHHMM(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil
}
