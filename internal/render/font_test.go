package render

import "testing"

func TestLoadFontAndMeasure(t *testing.T) {
	f, err := LoadFont(RegularTTF, 10)
	if err != nil {
		t.Fatal(err)
	}
	w, h := f.Measure("12:34 London Paddington")
	if w <= 0 || h <= 0 {
		t.Fatalf("Measure returned non-positive size: %dx%d", w, h)
	}
	if h > 14 {
		t.Fatalf("10px font row height %d unexpectedly tall", h)
	}
}

func TestRenderTextProducesInk(t *testing.T) {
	f, err := LoadFont(BoldTTF, 10)
	if err != nil {
		t.Fatal(err)
	}
	img := f.RenderText("Platform 1")
	ink := 0
	for _, a := range img.Pix {
		if a > 0 {
			ink++
		}
	}
	if ink == 0 {
		t.Fatal("RenderText produced a blank bitmap")
	}
}

func TestGoldenHarness(t *testing.T) {
	f, err := LoadFont(BoldTTF, 20)
	if err != nil {
		t.Fatal(err)
	}
	fb := New(256, 64)
	fb.BlitAlpha(f.RenderText("12:34"), 4, 4, 15) // BlitAlpha lands in Task 9
	assertGolden(t, "harness_smoke", fb)
}
