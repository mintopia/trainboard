package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func frame(t *testing.T, s *render.Scene, tick int) *render.Framebuffer {
	t.Helper()
	fb := render.New(W, H)
	s.Render(fb, tick, fixedNow)
	return fb
}

func TestInitialisingGolden(t *testing.T) {
	s := initialisingScene("v1.2.3", mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_initialising", frame(t, s, 0))
}

func TestNoServicesGoldenDefaultText(t *testing.T) {
	s := noServicesScene(emptyBoard(), mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_noservices_default", frame(t, s, 0))
}

func TestNoServicesCarouselShowsMessagePage(t *testing.T) {
	f := mustFonts(t)
	s := noServicesScene(emptyBoard(), f)
	// During the first page window (tick just past nsDefaultTicks).
	rendertest.AssertGolden(t, "testdata", "scene_noservices_page0", frame(t, s, nsDefaultTicks+1))
	// Default and page frames must differ.
	if string(frame(t, s, 0).Pix) == string(frame(t, s, nsDefaultTicks+1).Pix) {
		t.Fatal("carousel page must differ from default text")
	}
}

func TestNoServicesCarouselCyclesBackToDefault(t *testing.T) {
	f := mustFonts(t)
	b := emptyBoard() // one message => pages = wordwrap pages of that message
	s := noServicesScene(b, f)
	pages := len(splitPages(wordwrap(f.Regular, W, b.Messages[0])))
	cycle := nsDefaultTicks + pages*nsPageTicks
	if string(frame(t, s, 0).Pix) != string(frame(t, s, cycle).Pix) {
		t.Fatal("carousel must return to default text after all pages")
	}
}

func TestNoServicesNoMessagesAlwaysDefault(t *testing.T) {
	b := emptyBoard()
	b.Messages = nil
	s := noServicesScene(b, mustFonts(t))
	if string(frame(t, s, 0).Pix) != string(frame(t, s, 5000).Pix) {
		t.Fatal("without messages the body must be static")
	}
}

func TestErrorSceneGolden(t *testing.T) {
	s := errorScene(obs.FaultDarwinUnreachable, mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_error_e01", frame(t, s, 0))
}

func TestClockNotSyncedGolden(t *testing.T) {
	s := clockNotSyncedScene(mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_clocknotsynced", frame(t, s, 0))
}

func TestHotspotInfoGolden(t *testing.T) {
	s := hotspotInfoScene("trainboard-setup", "hunter22", "192.168.4.1", mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_hotspot", frame(t, s, 0))
}

func TestHotspotInfoShowsPassword(t *testing.T) {
	s := hotspotInfoScene("trainboard-setup", "hunter22", "192.168.4.1", mustFonts(t))
	withPass := frame(t, s, 0)

	s2 := hotspotInfoScene("trainboard-setup", "different", "192.168.4.1", mustFonts(t))
	withOtherPass := frame(t, s2, 0)

	if string(withPass.Pix) == string(withOtherPass.Pix) {
		t.Fatal("changing the password must change the rendered frame")
	}
}
