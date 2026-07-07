package config

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// apAlphabet excludes visually ambiguous characters (0/O, 1/l/I) so an
// operator copying the AP-mode password off the panel or the config page
// isn't tripped up by them. Shared by GenerateAPPassword's callers: web's
// RegenerateAPPassword (an authenticated admin action) and the
// --manage-network wiring in cmd/trainboard, which generates one
// automatically the first time an unconfigured device needs to show a
// hotspot (Task 12).
const apAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// apPasswordLen matches the length web.RegenerateAPPassword has always
// minted (12 symbols from apAlphabet, ~61 bits of entropy).
const apPasswordLen = 12

// GenerateAPPassword mints a fresh random AP-mode password. It does not
// read or write any config file — callers decide whether/how to persist the
// result (see ProvisioningConfig.APPassword, Save, SaveConnectivity).
func GenerateAPPassword() (string, error) {
	buf := make([]byte, apPasswordLen)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(apAlphabet))))
		if err != nil {
			return "", fmt.Errorf("config: generate ap password: %w", err)
		}
		buf[i] = apAlphabet[n.Int64()]
	}
	return string(buf), nil
}
