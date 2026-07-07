// Command bench times SSD1322 flush paths on real hardware to decide the
// render architecture (full-frame vs dirty-region). Runs on the Pi only.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mintopia/trainboard/internal/display"
	"github.com/mintopia/trainboard/internal/render"
)

func main() {
	frames := flag.Int("frames", 300, "frames per measurement")
	hz := flag.Int64("hz", 16_000_000, "SPI clock in Hz")
	spiPort := flag.String("spi", "SPI0.0", "SPI port name")
	dc := flag.String("dc", "GPIO24", "D/C GPIO pin")
	rst := flag.String("rst", "GPIO25", "reset GPIO pin")
	flag.Parse()

	tr, err := display.OpenPeriph(display.PeriphConfig{SPIPort: *spiPort, DCPin: *dc, ResetPin: *rst, MaxHz: *hz})
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer func() { _ = tr.Close() }()
	d := display.New(tr)
	if err := d.Init(); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}

	fb := render.New(256, 64)
	for i := range fb.Pix {
		fb.Pix[i] = byte(i % 16) // non-trivial pattern
	}
	full := fb.Pack()

	measure := func(name string, n int, fn func() error) {
		start := time.Now()
		for i := 0; i < n; i++ {
			if err := fn(); err != nil {
				fmt.Fprintln(os.Stderr, name, "err:", err)
				os.Exit(1)
			}
		}
		el := time.Since(start)
		per := el / time.Duration(n)
		fmt.Printf("%-16s %d frames  %8.3f ms/frame  %6.1f fps\n",
			name, n, float64(per.Microseconds())/1000, float64(time.Second)/float64(per))
	}

	// Partial-flush row bands (4-pixel-aligned full width).
	band12 := make([]byte, 12*(256/2))
	band24 := make([]byte, 24*(256/2))

	measure("full-frame", *frames, func() error { return d.Flush(full) })
	measure("region-256x12", *frames, func() error { return d.FlushRegion(band12, 0, 0, 256, 12) })
	measure("region-256x24", *frames, func() error { return d.FlushRegion(band24, 0, 0, 256, 24) })
}
