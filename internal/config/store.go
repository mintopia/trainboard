package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultPath is the on-device config location.
const DefaultPath = "/var/lib/trainboard/config.json"

// Load reads and validates the config at path. A missing file returns defaults
// with no error; a present-but-invalid file returns an error.
// Note: Default() is itself invalid (empty origin/token), so callers deciding
// provisioning/AP-mode must Validate() the returned config, not rely on Load's
// error alone.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("config: parsing %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save validates c with the full Validate() tier, then writes it atomically
// at mode 0600: a temp file in the same directory is written, fsync'd, and
// renamed over path. Use SaveConnectivity instead when c is only expected to
// meet the lighter ValidateConnectivity bar (e.g. an unconfigured device
// that hasn't been through /setup yet).
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return saveRaw(path, c)
}

// SaveConnectivity validates c against ValidateConnectivity (not the full
// Validate) and writes it the same atomic way as Save. This is the tier
// --manage-network wiring uses to persist a freshly generated
// Provisioning.APPassword on a device that hasn't completed first-boot setup
// (Board.Origin/Darwin.Token unset, so full Validate would reject it) — see
// Task 12's report for the investigation this rests on: config.Save always
// hard-validates, so a distinct save path was needed for the connectivity
// tier rather than relaxing Save's existing contract.
func SaveConnectivity(path string, c Config) error {
	if err := c.ValidateConnectivity(); err != nil {
		return err
	}
	return saveRaw(path, c)
}

// saveRaw writes c to path as an atomic rename-over, with no validation of
// its own — callers (Save, SaveConnectivity) pick the validation tier.
func saveRaw(path string, c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: writing temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: closing temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: renaming into place: %w", err)
	}
	return nil
}
