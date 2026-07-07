package web

import (
	"errors"
	"fmt"
	"log/slog"
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
	// SoakRemaining reports the burn-in soak's remaining time (0 =
	// inactive). Optional: nil reads as never-soaking.
	SoakRemaining func() time.Duration
}

// Actions are the write-side callbacks main wires up. Apply is invoked after
// a config save (production: delayed clean exit → systemd restart).
type Actions struct {
	Apply  func()
	Reboot func() error
	// SoakStart / SoakCancel drive the burn-in soak (runtime.Soak in
	// production). Optional: a nil SoakStart makes StartSoak error.
	SoakStart  func(d time.Duration)
	SoakCancel func()
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
	Version       string
	Uptime        time.Duration
	State         string
	Fault         string
	LastFetch     time.Time
	HasSnapshot   bool
	IPs           []string
	Events        []obs.Event
	SoakRemaining time.Duration
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
	st.SoakRemaining = s.SoakRemaining()
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
		if len(u.NewPassword) < 8 {
			return errors.New("password must be at least 8 characters")
		}
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
// originCRS is required (a 3-letter CRS code). token is write-only like the
// other secrets: a blank token keeps whatever is already stored on disk (the
// path taken when a config already carries a valid token and setup is only
// establishing the admin password). It is NOT optional in the sense of
// "the board can run without one" — a genuinely virgin device has no stored
// token, so a blank token there leaves Darwin.Token empty and the closing
// cur.Validate() call rejects it with "darwin.token is required"; no config
// file is written in that case. Callers driving a first-boot UI (no config
// on disk yet) must collect a real token from the operator.
//
// Permitted only while no admin password is stored yet.
func (s *Service) SetInitialPassword(pw, originCRS, token string) error {
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	// config.Load validates internally, so a present-but-invalid file (e.g.
	// an installer-written config.Default() with no origin/token yet, or a
	// corrupted/unreadable file) errors here rather than returning a usable
	// document. Falling back to Default() in every such case is deliberate:
	// first-boot setup's whole job is turning an invalid-or-absent document
	// into a valid one, and a config file this method can't even parse is
	// treated the same as "no config yet" — fresh setup over an unrecoverable
	// file, rather than surfacing a load error the operator can't act on
	// from this form.
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

// RegenerateAPPassword mints, stores, and returns a fresh AP-mode password.
// The value is displayed once; ConfigRedacted never returns it. Generation
// itself lives in config.GenerateAPPassword (Task 12 refactor) so the
// --manage-network wiring in cmd/trainboard can mint one the same way
// without depending on this package.
func (s *Service) RegenerateAPPassword() (string, error) {
	pw, err := config.GenerateAPPassword()
	if err != nil {
		return "", err
	}
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

// soakDurations are the only soak lengths the UI offers; both the HTML form
// and the JSON API validate against this set (spec: 1h/4h/8h, picked at
// start).
var soakDurations = map[string]time.Duration{
	"1h": time.Hour,
	"4h": 4 * time.Hour,
	"8h": 8 * time.Hour,
}

// StartSoak validates the requested duration key and starts the soak.
func (s *Service) StartSoak(key string) error {
	d, ok := soakDurations[key]
	if !ok {
		return fmt.Errorf("invalid soak duration %q (want 1h, 4h or 8h)", key)
	}
	if s.act.SoakStart == nil {
		return errors.New("soak is not available")
	}
	s.act.SoakStart(d)
	return nil
}

// CancelSoak ends any running soak; idle cancel is a no-op.
func (s *Service) CancelSoak() {
	if s.act.SoakCancel != nil {
		s.act.SoakCancel()
	}
}

// SoakRemaining reports the running soak's remaining time (0 = inactive).
func (s *Service) SoakRemaining() time.Duration {
	if s.src.SoakRemaining == nil {
		return 0
	}
	return s.src.SoakRemaining()
}
