package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"

	"github.com/mintopia/trainboard/internal/board"
)

// previewSink is the host-mode Flusher (and, teed with the panel, the
// production preview source for the web UI): it unpacks SSD1322 wire frames
// and rate-limits how often it encodes a PNG. When dir is non-empty each
// encoded frame is also written to disk at dir/frame.png (atomically, via a
// temp file + rename) so the CLI's --preview-dir keeps working; when dir ==
// "" (production) encoding still happens for Latest(), but disk is never
// touched at all.
type previewSink struct {
	dir          string
	every        int // write 1 PNG per N flushes
	n            int
	lastContrast byte

	mu     sync.Mutex
	latest []byte // most recently encoded PNG; never mutated once published
}

func newPreviewSink(dir string, every int) *previewSink {
	return &previewSink{dir: dir, every: every}
}

func (p *previewSink) SetContrast(level byte) error {
	p.lastContrast = level
	return nil
}

// Latest returns the bytes of the most recently encoded PNG, or nil if no
// frame has been encoded yet. The returned slice is an immutable snapshot:
// every new frame allocates a fresh buffer and swaps the pointer under
// mu — it never reuses or appends to a previously returned slice — because
// callers (the web UI's /preview.png handler) write it directly to an HTTP
// response without copying.
func (p *previewSink) Latest() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.latest
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
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	// A fresh slice each time: bytes.Buffer.Bytes() plus everything below
	// never touches this allocation again, so publishing it via Latest()
	// hands callers an immutable snapshot.
	encoded := buf.Bytes()

	p.mu.Lock()
	p.latest = encoded
	p.mu.Unlock()

	if p.dir == "" {
		return nil
	}
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
