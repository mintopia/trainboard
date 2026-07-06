// Package web is the embedded admin UI: HTTP server, session auth,
// middleware, and the service layer over config/status/actions.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: OWASP low-memory profile, sized for the Pi Zero 2 W.
const (
	argonTime      = 2
	argonMemoryKiB = 19456
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16
)

// HashPassword returns a PHC-format argon2id hash with a random salt.
func HashPassword(password string) (string, error) {
	return hashWithParams(password, argonTime, argonMemoryKiB, argonThreads)
}

func hashWithParams(password string, time, memoryKiB uint32, threads uint8) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("web: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, time, memoryKiB, threads, argonKeyLen)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, time, threads, b64(salt), b64(key)), nil
}

// VerifyPassword reports whether password matches the PHC-format hash.
// Parameters are read from the hash itself; malformed input returns false.
func VerifyPassword(phc, password string) bool {
	parts := strings.Split(phc, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, key]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
