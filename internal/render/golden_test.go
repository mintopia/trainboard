package render

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden images")

// toGray renders the framebuffer as an 8-bit grey image (level*17 ⇒ 0..255).
func toGray(fb *Framebuffer) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, fb.W, fb.H))
	for i, lvl := range fb.Pix {
		g.Pix[i] = lvl * 17
	}
	return g
}

// assertGolden compares fb against testdata/<name>.png, or regenerates it
// under -update.
func assertGolden(t *testing.T, name string, fb *Framebuffer) {
	t.Helper()
	path := "testdata/" + name + ".png"
	g := toGray(fb)
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

// TestGoldenHarnessSelfTest exercises assertGolden/toGray directly (without
// BlitAlpha, which lands in a later task) so the harness itself is covered
// and the helpers above aren't flagged as unused.
func TestGoldenHarnessSelfTest(t *testing.T) {
	fb := New(16, 8)
	// simple recognizable pattern: a diagonal + border pixel
	fb.SetPixel(0, 0, 15)
	fb.SetPixel(15, 7, 15)
	for i := 0; i < 8; i++ {
		fb.SetPixel(i, i%8, byte(i))
	}
	assertGolden(t, "harness_selftest", fb)
}
