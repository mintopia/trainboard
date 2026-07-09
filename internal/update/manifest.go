package update

import (
	"encoding/json"
	"fmt"

	"golang.org/x/mod/semver"
)

// RequiredArch is the only architecture this device installs. It is checked
// against the SIGNED manifest, so a mismatched-arch asset can't be swapped
// in even with a valid release signature (spec §1).
const RequiredArch = "linux/arm64"

// Manifest is the signed release descriptor — the ONLY signed object; it
// binds everything else (spec §1). sha256 is of the DECOMPRESSED binary.
type Manifest struct {
	Version    string `json:"version"`
	Channel    string `json:"channel"`
	Commit     string `json:"commit"`
	Arch       string `json:"arch"`
	Asset      string `json:"asset"`
	SHA256     string `json:"sha256"`
	MinVersion string `json:"min_version"`
}

// ParseManifest decodes a manifest document. Verify the signature FIRST
// (VerifyManifest) — parsing is not validation.
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("update: parsing manifest: %w", err)
	}
	return m, nil
}

// CheckInstallable runs the three anti-rollback/replay checks from spec §1
// against a signature-verified manifest:
//
//  1. arch must match this device;
//  2. version must be strictly newer than running — except that a
//     non-semver running version ("dev" builds, a just-migrated device)
//     never blocks an upgrade;
//  3. version must be at or above the device's persisted version floor, so
//     a validly-signed OLD manifest can't be replayed to downgrade.
//
// There are deliberately no time-based checks (#17).
func (m Manifest) CheckInstallable(running, floor string) error {
	if m.Arch != RequiredArch {
		return fmt.Errorf("update: manifest arch %q does not match device arch %q", m.Arch, RequiredArch)
	}
	if !semver.IsValid(m.Version) {
		return fmt.Errorf("update: manifest version %q is not valid semver", m.Version)
	}
	if m.MinVersion != "" && !semver.IsValid(m.MinVersion) {
		return fmt.Errorf("update: manifest min_version %q is not valid semver", m.MinVersion)
	}
	if m.Asset == "" {
		return fmt.Errorf("update: manifest has no asset name")
	}
	if m.SHA256 == "" {
		return fmt.Errorf("update: manifest has no sha256")
	}
	if semver.IsValid(running) && semver.Compare(m.Version, running) <= 0 {
		return fmt.Errorf("update: %s is not newer than running %s", m.Version, running)
	}
	if semver.IsValid(floor) && semver.Compare(m.Version, floor) < 0 {
		return fmt.Errorf("update: %s is below the version floor %s (rollback protection)", m.Version, floor)
	}
	return nil
}

// maxVersion returns the semver-greater of a and b; an invalid (or empty)
// side loses. Used to ratchet the persisted version floor monotonically.
func maxVersion(a, b string) string {
	switch {
	case !semver.IsValid(a):
		return b
	case !semver.IsValid(b):
		return a
	case semver.Compare(a, b) >= 0:
		return a
	default:
		return b
	}
}
