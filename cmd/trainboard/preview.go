package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/mintopia/trainboard/internal/board"
)

// previewSink is the host-mode Flusher: it unpacks SSD1322 wire frames and
// writes a rate-limited PNG the operator (and later the M2 status page) can
// watch instead of real glass.
type previewSink struct {
	dir          string
	every        int // write 1 PNG per N flushes
	n            int
	lastContrast byte
}

func newPreviewSink(dir string, every int) *previewSink {
	return &previewSink{dir: dir, every: every}
}

func (p *previewSink) SetContrast(level byte) error {
	p.lastContrast = level
	return nil
}

func (p *previewSink) Flush(packed []byte) error {
	p.n++
	if p.n%p.every != 0 {
		return nil
	}
	img := image.NewGray(image.Rect(0, 0, board.W, board.H))
	for i, b := range packed {
		img.Pix[i*2] = (b >> 4) * 17 // high nibble = left pixel
		img.Pix[i*2+1] = (b & 0x0F) * 17
	}
	tmp, err := os.CreateTemp(p.dir, "frame-*.png.tmp")
	if err != nil {
		return err
	}
	if err := png.Encode(tmp, img); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(p.dir, "frame.png"))
}
