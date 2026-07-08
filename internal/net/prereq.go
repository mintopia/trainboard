package net

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// CheckPrereqs verifies first-boot radio prerequisites (issue #6): rfkill
// soft-block state via sysfs (rfkill binary is not installed on DietPi) and
// regulatory country via `iw reg get`. Returns nil or an error suitable for
// FaultRadioBlocked. It FIXES what it safely can (writes "0" to the sysfs
// soft file; `iw reg set <country>` when country is 00/unset) and
// re-verifies. country is the configured regulatory domain (config.Wifi.
// Country, defaulted to "GB" by the caller when empty).
func CheckPrereqs(ctx context.Context, r Runner, country string, readFile func(string) ([]byte, error), writeFile func(string, []byte) error, glob func(string) ([]string, error)) error {
	// Check rfkill soft-block state
	if err := checkRfkill(ctx, readFile, writeFile, glob); err != nil {
		return err
	}

	// Check regulatory country
	if err := checkRegulatory(ctx, r, country); err != nil {
		return err
	}

	return nil
}

// checkRfkill verifies that no wlan rfkill device is soft-blocked. Zero
// matches for the glob pattern (typeFiles == nil, err == nil) means no
// rfkill devices exist at all — nothing to block, so that's a genuine pass.
// A glob (or read) error is a different thing entirely: it means we could
// not determine the device list/state, so we cannot vouch for it being
// unblocked and must fail loudly instead of assuming the best.
func checkRfkill(_ context.Context, readFile func(string) ([]byte, error), writeFile func(string, []byte) error, glob func(string) ([]string, error)) error {
	// Glob all rfkill type files
	pattern := "/sys/class/rfkill/rfkill*/type"
	typeFiles, err := glob(pattern)
	if err != nil {
		return fmt.Errorf("could not enumerate rfkill devices (glob %s failed): %w", pattern, err)
	}

	for _, typeFile := range typeFiles {
		typeData, err := readFile(typeFile)
		if err != nil {
			return fmt.Errorf("could not read rfkill type file %s: %w", typeFile, err)
		}

		// Only care about wlan devices
		if strings.TrimSpace(string(typeData)) != "wlan" {
			continue
		}

		// Get the corresponding soft file
		dir := filepath.Dir(typeFile)
		softFile := filepath.Join(dir, "soft")

		// Check if soft-blocked
		softData, err := readFile(softFile)
		if err != nil {
			return fmt.Errorf("could not read rfkill soft-block state %s: %w", softFile, err)
		}

		if strings.TrimSpace(string(softData)) == "1" {
			// Try to unblock
			if err := writeFile(softFile, []byte("0")); err != nil {
				return fmt.Errorf("rfkill soft-block persists (failed to write to %s): %w", softFile, err)
			}

			// Re-read to verify unblock worked
			softData, err := readFile(softFile)
			if err != nil {
				return fmt.Errorf("rfkill soft-block persists (failed to re-read %s): %w", softFile, err)
			}

			if strings.TrimSpace(string(softData)) == "1" {
				return fmt.Errorf("rfkill soft-block persists on %s; check if a hardware switch or BIOS setting is blocking the radio", softFile)
			}
		}
	}

	return nil
}

// checkRegulatory verifies the regulatory country is set (not 00), fixing it
// to country when it isn't.
func checkRegulatory(ctx context.Context, r Runner, country string) error {
	// Get current regulatory domain
	out, err := r.Run(ctx, "iw", "reg", "get")
	if err != nil {
		return fmt.Errorf("could not check regulatory domain (iw reg get failed): %w", err)
	}

	// Check if country is 00 (unset)
	if strings.Contains(out, "country 00") {
		// Try to set to the configured country
		_, err := r.Run(ctx, "iw", "reg", "set", country)
		if err != nil {
			// If set fails, just report the issue
			return fmt.Errorf("regulatory domain is unset (country 00) and setting to %s failed: %w", country, err)
		}

		// Re-check after setting
		out, err := r.Run(ctx, "iw", "reg", "get")
		if err != nil {
			return fmt.Errorf("regulatory domain could not be verified after setting: %w", err)
		}

		if strings.Contains(out, "country 00") {
			return fmt.Errorf("regulatory domain remains unset (country 00); check regulatory support or try: sudo iw reg set %s", country)
		}
	}

	return nil
}
