package display

import "testing"

// PeriphTransport must satisfy the Transport interface at compile time.
var _ Transport = (*PeriphTransport)(nil)

func TestOpenPeriphMissingDeviceErrors(t *testing.T) {
	// No SPI hardware in CI: opening a bogus port must error, not panic.
	_, err := OpenPeriph(PeriphConfig{SPIPort: "SPI9.9", DCPin: "GPIO25", ResetPin: "GPIO27", MaxHz: 16_000_000})
	if err == nil {
		t.Fatal("expected error opening nonexistent SPI port")
	}
}
