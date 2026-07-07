package board

import (
	"time"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/render"
)

// NoServices carousel timing (ticks): the default text holds ~10s, each
// message page ~5s (reference settings.messages.{frequency,interval}).
const (
	nsDefaultTicks = 250
	nsPageTicks    = 125
)

const noServicesText = "No services available at this time."

func centered(f *render.Font, text string, y int) render.Element {
	return &render.StaticText{Font: f, Text: text, X: 0, Y: y, W: W, H: RowH, Align: render.AlignCenter, Level: 15}
}

// initialisingScene is the pre-first-data boot screen.
func initialisingScene(version string, f *Fonts) *render.Scene {
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, "Departure board is initialising", 0),
		centered(f.Regular, "Version: "+version, 16),
		centered(f.Regular, "Connecting...", 28),
	}}
}

// noServicesBody carousels the default text and 3-line NRCC message pages.
type noServicesBody struct {
	font  *render.Font
	pages [][]string
}

func (nb *noServicesBody) Render(fb *render.Framebuffer, tick int, now time.Time) {
	lines := []string{noServicesText}
	if len(nb.pages) > 0 {
		cycle := nsDefaultTicks + len(nb.pages)*nsPageTicks
		phase := tick % cycle
		if phase >= nsDefaultTicks {
			lines = nb.pages[(phase-nsDefaultTicks)/nsPageTicks]
		}
	}
	for i, ln := range lines {
		centered(nb.font, ln, RowH*(i+1)).Render(fb, tick, now)
	}
}

// noServicesScene shows the station title plus the NRCC message carousel.
func noServicesScene(b *data.Board, f *Fonts) *render.Scene {
	var pages [][]string
	for _, msg := range b.Messages {
		pages = append(pages, splitPages(wordwrap(f.Regular, W, msg))...)
	}
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, b.LocationName, 0),
		&noServicesBody{font: f.Regular, pages: pages},
		offsetElement(&render.Clock{Large: f.BoldLarge, Tall: f.BoldTall, W: W, Level: 15}, 0, ClockY, W, ClockH),
	}}
}

func faultCorner(fault obs.FaultCode, f *Fonts) render.Element {
	return &render.StaticText{Font: f.Regular, Text: string(fault), X: ColStatusX, Y: 52, W: ColStatusW, H: RowH, Align: render.AlignRight, Level: 15}
}

// errorScene is shown on hard fetch failure after the stale grace expires.
// An optional detail line (e.g. the failing connectivity stage for E06) is
// rendered at y=36, between the message (24) and the fault corner (52).
func errorScene(fault obs.FaultCode, f *Fonts, detail ...string) *render.Scene {
	els := []render.Element{
		centered(f.Bold, "Unable to fetch departures", 8),
		centered(f.Regular, fault.Message(), 24),
	}
	if len(detail) > 0 && detail[0] != "" {
		els = append(els, centered(f.Regular, detail[0], 36))
	}
	els = append(els, faultCorner(fault, f))
	return &render.Scene{Elements: els}
}

// clockNotSyncedScene is the pre-NTP transient; deliberately clockless.
func clockNotSyncedScene(f *Fonts) *render.Scene {
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, "Waiting for time sync...", 20),
		faultCorner(obs.FaultClockNotSynced, f),
	}}
}

// hotspotInfoScene is the first-boot / AP-fallback setup screen: it shows
// the join details (SSID + password) and the captive-portal address.
func hotspotInfoScene(ssid, password, addr string, f *Fonts) *render.Scene {
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, "Setup mode", 0),
		centered(f.Regular, "Join hotspot: "+ssid, 16),
		centered(f.Regular, "Then open http://"+addr, 28),
		centered(f.Regular, "Password: "+password, 40),
	}}
}
