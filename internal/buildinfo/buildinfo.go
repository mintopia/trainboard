// Package buildinfo exposes static identifiers for the binary.
package buildinfo //nolint:revive // internal package; does not collide with any import of stdlib debug/buildinfo

// Name is the canonical short name of the application.
func Name() string { return "trainboard" }
