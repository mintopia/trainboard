// Package buildinfo exposes static identifiers for the binary.
package buildinfo //nolint:revive // internal package; does not collide with any import of stdlib debug/buildinfo

// Name is the canonical short name of the application.
func Name() string { return "trainboard" }

// version is stamped at release build time via
// -ldflags "-X github.com/mintopia/trainboard/internal/buildinfo.version=vX.Y.Z".
var version = "dev"

// Version reports the release version of this binary.
func Version() string { return version }
