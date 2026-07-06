// Package rendertest provides the golden-image test harness for packages
// downstream of render (board, …). It intentionally mirrors the private
// harness in render's own golden_test.go, which cannot import this package
// (render's tests are in-package; importing rendertest would be an import
// cycle). Keep the two in lockstep. Import from _test files only.
package rendertest

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/render"
)

var update = flag.Bool("update", false, "regenerate golden images")

// ToGray converts the 4-bit framebuffer to an 8-bit grayscale image
// (level × 17, so 15 → 255) for PNG golden storage.
func ToGray(fb *render.Framebuffer) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, fb.W, fb.H))
	for i, lv := range fb.Pix {
		g.Pix[i] = lv * 17
	}
	return g
}

// AssertGolden compares fb against dir/name.png byte-exactly, regenerating
// the file when -update is set.
func AssertGolden(t *testing.T, dir, name string, fb *render.Framebuffer) {
	t.Helper()
	path := filepath.Join(dir, name+".png")
	g := ToGray(fb)
	if *update {
		var buf bytes.Buffer
		if err := png.Encode(&buf, g); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run: go test -run %s -update): %v", path, t.Name(), err)
	}
	want, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	wg, ok := want.(*image.Gray)
	if !ok || wg.Rect != g.Rect || !bytes.Equal(wg.Pix, g.Pix) {
		t.Fatalf("framebuffer differs from golden %s", path)
	}
}
