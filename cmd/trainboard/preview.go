package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/mintopia/trainboard/internal/board"
)

// previewSink is the host-mode (--preview-dir) Flusher: it unpacks SSD1322
// wire frames and rate-limits how often it encodes a PNG, writing each
// encoded frame to disk at dir/frame.png (atomically, via a temp file +
// rename) so a developer without real hardware can watch the board render.
// It is never used in production — the web UI's live board (Task 4) renders
// client-side from GET /api/board's JSON, not a server-streamed PNG, so
// production drives the panel directly with no preview sink in the loop.
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
	if p.dir == "" {
		return nil
	}
	img := image.NewGray(image.Rect(0, 0, board.W, board.H))
	for i, b := range packed {
		img.Pix[i*2] = (b >> 4) * 17 // high nibble = left pixel
		img.Pix[i*2+1] = (b & 0x0F) * 17
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	encoded := buf.Bytes()

	tmp, err := os.CreateTemp(p.dir, "frame-*.png.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
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
