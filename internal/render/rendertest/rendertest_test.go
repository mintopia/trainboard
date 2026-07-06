package rendertest_test

import (
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func TestToGrayScalesLevels(t *testing.T) {
	fb := render.New(2, 1)
	fb.SetPixel(0, 0, 15)
	fb.SetPixel(1, 0, 8)
	g := rendertest.ToGray(fb)
	if g.Pix[0] != 255 || g.Pix[1] != 8*17 {
		t.Fatalf("pix = %v, want [255 136]", g.Pix[:2])
	}
}

func TestAssertGoldenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fb := render.New(4, 4)
	fb.SetPixel(1, 2, 9)
	// Write the golden by hand, then assert against it.
	writeGolden(t, dir, "rt", fb)
	rendertest.AssertGolden(t, dir, "rt", fb)
}

// writeGolden encodes exactly what AssertGolden -update would write.
func writeGolden(t *testing.T, dir, name string, fb *render.Framebuffer) {
	t.Helper()
	g := rendertest.ToGray(fb)
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := encodePNG(f, g); err != nil {
		t.Fatal(err)
	}
}

// encodePNG is a one-line png.Encode wrapper.
func encodePNG(w io.Writer, img *image.Gray) error {
	return png.Encode(w, img)
}
