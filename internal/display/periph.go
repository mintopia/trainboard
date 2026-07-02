package display

import (
	"fmt"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

// PeriphConfig locates the SPI port and control GPIOs for the panel.
type PeriphConfig struct {
	SPIPort  string // e.g. "SPI0.0"
	DCPin    string // data/command select, e.g. "GPIO25"
	ResetPin string // reset, e.g. "GPIO27"
	MaxHz    int64  // SPI clock, e.g. 16_000_000
}

// PeriphTransport drives the SSD1322 over periph.io SPI + GPIO.
type PeriphTransport struct {
	port spi.PortCloser
	conn spi.Conn
	dc   gpio.PinIO
	rst  gpio.PinIO
}

// OpenPeriph initializes the host, opens the SPI port and control pins.
func OpenPeriph(cfg PeriphConfig) (*PeriphTransport, error) {
	if _, err := host.Init(); err != nil {
		return nil, fmt.Errorf("periph host init: %w", err)
	}
	port, err := spireg.Open(cfg.SPIPort)
	if err != nil {
		return nil, fmt.Errorf("open spi %q: %w", cfg.SPIPort, err)
	}
	conn, err := port.Connect(physic.Frequency(cfg.MaxHz)*physic.Hertz, spi.Mode0, 8)
	if err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("spi connect: %w", err)
	}
	dc := gpioreg.ByName(cfg.DCPin)
	rst := gpioreg.ByName(cfg.ResetPin)
	if dc == nil || rst == nil {
		_ = port.Close()
		return nil, fmt.Errorf("gpio pin not found (dc=%q rst=%q)", cfg.DCPin, cfg.ResetPin)
	}
	return &PeriphTransport{port: port, conn: conn, dc: dc, rst: rst}, nil
}

// Command sends an opcode (D/C low) followed by any args (D/C high).
func (p *PeriphTransport) Command(cmd byte, args ...byte) error {
	if err := p.dc.Out(gpio.Low); err != nil {
		return err
	}
	if err := p.conn.Tx([]byte{cmd}, nil); err != nil {
		return err
	}
	if len(args) > 0 {
		return p.Data(args)
	}
	return nil
}

// Data sends payload bytes (D/C high) in spidev-safe chunks.
func (p *PeriphTransport) Data(b []byte) error {
	if err := p.dc.Out(gpio.High); err != nil {
		return err
	}
	for _, c := range chunk(b, maxChunk) {
		if err := p.conn.Tx(c, nil); err != nil {
			return err
		}
	}
	return nil
}

// Reset pulses the reset line low then high.
func (p *PeriphTransport) Reset() error {
	if err := p.rst.Out(gpio.Low); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	if err := p.rst.Out(gpio.High); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)
	return nil
}

// Close releases the SPI port.
func (p *PeriphTransport) Close() error { return p.port.Close() }
