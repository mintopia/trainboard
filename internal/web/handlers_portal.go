package web

import (
	"fmt"
	"net/http"
)

// apSetupURL is the absolute setup URL used both by the captive-portal probe
// handlers below and by setupGate's AP-mode redirect. It is hardcoded to
// board.Hotspot's default AP address (192.168.4.1) rather than derived from
// Hotspot() itself: captive-portal probes are OS-hardcoded connectivity
// checks that need one stable, deterministic target regardless of which
// network stack answered, and it must match the on-screen URL a human sees.
const apSetupURL = "http://192.168.4.1/setup"

// isPortalProbePath reports whether p is one of the captive-portal probe
// endpoints registered by NewServer. setupGate consults this to let these
// three routes reach their own handlers un-redirected in every setupGate
// state (exactly like /static/) — they are deliberately pre-auth and
// pre-CSRF by design, since a just-associated phone has no session and never
// will until a human deliberately visits /setup.
func isPortalProbePath(p string) bool {
	switch p {
	case "/generate_204", "/hotspot-detect.html", "/ncsi.txt":
		return true
	default:
		return false
	}
}

// handleGenerate204 answers Android's captive-portal probe (GET
// /generate_204, which expects a bare "204 No Content" on a real network).
// In AP mode we deliberately answer something else — a 302 to the setup page
// — so Android's connectivity manager decides the network is captive and
// pops the "sign in to network" sheet, which is how a just-associated phone
// finds its way to /setup at all. Outside AP mode this path has no meaning:
// 404.
func (s *Server) handleGenerate204(w http.ResponseWriter, r *http.Request) {
	if s.svc.Hotspot() == nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, apSetupURL, http.StatusFound)
}

// handleHotspotDetect answers iOS/macOS's captive-portal probe (GET
// /hotspot-detect.html, which expects a page whose body is exactly
// "Success"). Unlike Android, Apple's Captive Network Assistant (CNA)
// renders whatever HTML comes back and follows links/meta-refreshes rather
// than reacting to a redirect status code — a 302 here can confuse older CNA
// versions into giving up rather than following it — so this answers 200
// with a meta-refresh plus a fallback link, both pointing at the setup page.
// Outside AP mode: 404.
func (s *Server) handleHotspotDetect(w http.ResponseWriter, r *http.Request) {
	if s.svc.Hotspot() == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	body := fmt.Sprintf(`<html><head><meta http-equiv="refresh" content="0;url=%s"></head>`+
		`<body>Redirecting to setup&#8230; <a href="%s">setup</a></body></html>`, apSetupURL, apSetupURL)
	_, _ = w.Write([]byte(body))
}

// handleNCSI answers Windows's captive-portal probe (GET /ncsi.txt, which
// expects a body of exactly "Microsoft NCSI"). Anything else — including a
// redirect — trips Windows's Network Connectivity Status Indicator into
// treating the network as requiring a browser sign-in. Outside AP mode: 404.
func (s *Server) handleNCSI(w http.ResponseWriter, r *http.Request) {
	if s.svc.Hotspot() == nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, apSetupURL, http.StatusFound)
}
