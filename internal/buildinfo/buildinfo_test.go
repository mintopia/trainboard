package buildinfo //nolint:revive // internal package; does not collide with any import of stdlib debug/buildinfo

import "testing"

func TestName(t *testing.T) {
	if got := Name(); got != "trainboard" {
		t.Fatalf("Name() = %q, want %q", got, "trainboard")
	}
}
