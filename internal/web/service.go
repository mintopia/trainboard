package web

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

// Sources are the read-side seams the UI renders from.
type Sources struct {
	Snapshot   func() *board.Snapshot
	Ring       *obs.Ring
	PreviewPNG func() []byte
	Version    string
	StartedAt  time.Time
}

// Actions are the write-side callbacks main wires up. Apply is invoked after
// a config save (production: delayed clean exit → systemd restart).
type Actions struct {
	Apply  func()
	Reboot func() error
}

// Service is the logic behind both the HTML pages and the JSON API.
type Service struct {
	cfgPath string
	src     Sources
	act     Actions
	log     *slog.Logger
}

// NewService wires the service to its config file and runtime sources.
func NewService(cfgPath string, src Sources, act Actions, log *slog.Logger) *Service {
	return &Service{cfgPath: cfgPath, src: src, act: act, log: log}
}

// StatusData is everything the status page/endpoint shows.
type StatusData struct {
	Version     string
	Uptime      time.Duration
	State       string
	Fault       string
	LastFetch   time.Time
	HasSnapshot bool
	IPs         []string
	Events      []obs.Event
}

const maxStatusEvents = 50

// Status assembles the live status view. Nil snapshot ⇒ initialising.
func (s *Service) Status() StatusData {
	st := StatusData{Version: s.src.Version, Uptime: time.Since(s.src.StartedAt), State: "initialising"}
	if snap := s.src.Snapshot(); snap != nil {
		st.HasSnapshot = true
		st.State = snap.State.String()
		st.Fault = string(snap.Fault)
		st.LastFetch = snap.FetchedAt
	}
	events := s.src.Ring.Events()
	for i := len(events) - 1; i >= 0 && len(st.Events) < maxStatusEvents; i-- {
		st.Events = append(st.Events, events[i])
	}
	st.IPs = localIPs()
	return st
}

func localIPs() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() {
			out = append(out, ipn.IP.String())
		}
	}
	return out
}

// ConfigRedacted loads the stored config with all secrets masked.
func (s *Service) ConfigRedacted() (config.Config, error) {
	c, err := config.Load(s.cfgPath)
	if err != nil {
		return config.Config{}, err
	}
	return c.Redacted(), nil
}

// ConfigUpdate carries a submitted config. Secret fields are write-only:
// empty means keep the stored value.
type ConfigUpdate struct {
	Cfg         config.Config
	NewToken    string
	NewWifiPSK  string
	NewPassword string
}

// UpdateConfig merges, validates, and transactionally saves. Nothing is
// written unless Validate passes.
func (s *Service) UpdateConfig(u ConfigUpdate) error {
	cur, err := config.Load(s.cfgPath)
	if err != nil {
		return err
	}
	next := cur
	next.Board = u.Cfg.Board
	next.Layout = u.Cfg.Layout
	next.Powersaving = u.Cfg.Powersaving
	next.Wifi.SSID = u.Cfg.Wifi.SSID
	if u.NewToken != "" {
		next.Darwin.Token = u.NewToken
	}
	if u.NewWifiPSK != "" {
		next.Wifi.PSK = u.NewWifiPSK
	}
	if u.NewPassword != "" {
		h, err := HashPassword(u.NewPassword)
		if err != nil {
			return err
		}
		next.Web.PasswordHash = h
	}
	if err := next.Validate(); err != nil {
		return err
	}
	return config.Save(s.cfgPath, next)
}

// SetInitialPassword completes first-boot setup. A virgin device has no
// Board.Origin/Darwin.Token yet, so this collects them alongside the
// password: config.Save (and Load, for an already-present file) validate
// internally, and Default() alone never passes Validate (empty origin/token).
// The resulting document must be valid on its own, so setup gathers whatever
// Validate requires up front rather than relaxing Save's contract.
//
// originCRS is required (a 3-letter CRS code); token is optional — leave it
// blank to configure the Darwin token later via UpdateConfig. Permitted only
// while no admin password is stored yet.
func (s *Service) SetInitialPassword(pw, originCRS, token string) error {
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	// config.Load validates internally, so a present-but-invalid file (e.g.
	// an installer-written config.Default() with no origin/token yet) errors
	// here rather than returning a usable document. That's exactly the
	// state first-boot setup exists to fix, so fall back to Default(): this
	// is the "current-or-default" load the design calls for.
	cur, err := config.Load(s.cfgPath)
	if err != nil {
		cur = config.Default()
	}
	if cur.Web.PasswordHash != "" {
		return errors.New("admin password is already set")
	}
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	cur.Web.PasswordHash = h
	cur.Board.Origin = originCRS
	if token != "" {
		cur.Darwin.Token = token
	}
	if err := cur.Validate(); err != nil {
		return err
	}
	return config.Save(s.cfgPath, cur)
}

// NeedsSetup reports whether first-boot setup still needs to run, i.e. no
// admin password is stored yet. It must not go through ConfigRedacted:
// config.Load validates internally and errors on a virgin or otherwise
// invalid on-disk file, which is exactly the state setup exists to fix. So
// this loads tolerantly instead: any Load error (missing-but-invalid,
// unparseable, failing Validate) is treated as "setup needed"; a successful
// Load needs setup iff no password hash is stored.
func (s *Service) NeedsSetup() bool {
	cur, err := config.Load(s.cfgPath)
	if err != nil {
		return true
	}
	return cur.Web.PasswordHash == ""
}

// VerifyLogin checks a login attempt against the stored hash.
func (s *Service) VerifyLogin(pw string) bool {
	cur, err := config.Load(s.cfgPath)
	if err != nil || cur.Web.PasswordHash == "" {
		return false
	}
	return VerifyPassword(cur.Web.PasswordHash, pw)
}

const apAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// RegenerateAPPassword mints, stores, and returns a fresh AP-mode password.
// The value is displayed once; ConfigRedacted never returns it.
func (s *Service) RegenerateAPPassword() (string, error) {
	buf := make([]byte, 12)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(apAlphabet))))
		if err != nil {
			return "", err
		}
		buf[i] = apAlphabet[n.Int64()]
	}
	pw := string(buf)
	cur, err := config.Load(s.cfgPath)
	if err != nil {
		return "", err
	}
	cur.Provisioning.APPassword = pw
	if err := config.Save(s.cfgPath, cur); err != nil {
		return "", err
	}
	return pw, nil
}
