package update

import (
	"errors"
	"fmt"

	minisign "github.com/jedisct1/go-minisign"
)

// embeddedKeys is the device's trusted keyring: minisign public keys, one
// base64 line each (the second line of a .pub file). Two keys by design
// (spec §1): the CI signing key (GitHub Actions secret) and the offline
// recovery key (operator's password manager). A manifest is trusted if ANY
// key here signed it — that overlap is what makes key rotation shippable
// as a normal signed update.
//
// EMPTY until the key ceremony (deploy.md §Self-update key ceremony) runs;
// until then Keyring() errors and the updater reports itself unavailable.
var embeddedKeys = []string{}

// Keyring parses the embedded trusted keys.
func Keyring() ([]minisign.PublicKey, error) {
	if len(embeddedKeys) == 0 {
		return nil, errors.New("update: keyring is empty (key ceremony not run)")
	}
	return ParsePublicKeys(embeddedKeys)
}

// ParsePublicKeys parses base64 minisign public-key lines.
func ParsePublicKeys(lines []string) ([]minisign.PublicKey, error) {
	keys := make([]minisign.PublicKey, 0, len(lines))
	for i, l := range lines {
		k, err := minisign.NewPublicKey(l)
		if err != nil {
			return nil, fmt.Errorf("update: keyring entry %d: %w", i, err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// VerifyManifest checks that sigFile is a valid minisign signature over
// message by ANY key in keys. There are deliberately no time-based checks
// here (#17): trust is bounded by the signed version floor, not wall-clock
// expiry, because a headless Pi without RTC must verify updates before NTP.
func VerifyManifest(keys []minisign.PublicKey, message, sigFile []byte) error {
	if len(keys) == 0 {
		return errors.New("update: keyring is empty")
	}
	sig, err := minisign.DecodeSignature(string(sigFile))
	if err != nil {
		return fmt.Errorf("update: parsing manifest signature: %w", err)
	}
	for i := range keys {
		ok, err := keys[i].Verify(message, sig)
		if ok && err == nil {
			return nil
		}
	}
	return errors.New("update: manifest signature not made by any trusted key")
}
