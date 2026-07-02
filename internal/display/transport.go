// Package display drives an SSD1322 256x64 4-bit greyscale OLED over SPI.
package display

// Transport carries SSD1322 command and data bytes to the panel. Command
// bytes are framed with D/C low; args and Data payloads with D/C high.
type Transport interface {
	Command(cmd byte, args ...byte) error
	Data(p []byte) error
	Reset() error
	Close() error
}
