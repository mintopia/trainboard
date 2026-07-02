package render

import (
	"image"
	"testing"
	"time"
)

// solidBlock is a minimal test-only Element that paints an opaque w×h
// rectangle at (x,y) using the given level, via the same BlitAlpha path
// production elements use. It exists purely to give TestSceneRendersInOrder
// deterministic, glyph-antialiasing-free pixels to assert on.
type solidBlock struct {
	x, y, w, h int
	level      byte
}

var _ Element = (*solidBlock)(nil)

func (b *solidBlock) Render(fb *Framebuffer, _ int, _ time.Time) {
	img := image.NewAlpha(image.Rect(0, 0, b.w, b.h))
	for i := range img.Pix {
		img.Pix[i] = 255 // fully opaque
	}
	fb.BlitAlpha(img, b.x, b.y, b.level)
}

// TestSceneRendersInOrder verifies Scene.Render both (a) renders every
// element and (b) composites them back-to-front — later elements in
// Elements must overwrite earlier ones where they overlap, since
// Framebuffer.BlitAlpha overwrites its destination rect rather than
// blending.
func TestSceneRendersInOrder(t *testing.T) {
	fb := New(32, 32)
	first := &solidBlock{x: 0, y: 0, w: 10, h: 10, level: 5}
	second := &solidBlock{x: 5, y: 5, w: 10, h: 10, level: 12}
	scene := &Scene{Elements: []Element{first, second}}

	scene.Render(fb, 0, time.Time{})

	// Point only covered by the first element: proves it rendered at all.
	if got := fb.At(2, 2); got != 5 {
		t.Fatalf("first-only pixel (2,2) = %d, want 5", got)
	}
	// Point only covered by the second element: proves it rendered at all.
	if got := fb.At(12, 12); got != 12 {
		t.Fatalf("second-only pixel (12,12) = %d, want 12", got)
	}
	// Overlap point: must equal the LATER element's level, proving order
	// matters (second composited over first, not the reverse).
	if got := fb.At(7, 7); got != 12 {
		t.Fatalf("overlap pixel (7,7) = %d, want 12 (later element must win)", got)
	}
}
