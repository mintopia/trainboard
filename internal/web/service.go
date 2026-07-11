package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/update"
)

// Sources are the read-side seams the UI renders from.
type Sources struct {
	Snapshot  func() *board.Snapshot
	Ring      *obs.Ring
	Version   string
	StartedAt time.Time
	// SoakRemaining reports the burn-in soak's remaining time (0 =
	// inactive). Optional: nil reads as never-soaking.
	SoakRemaining func() time.Duration
	// Hotspot reports the connectivity manager's AP-mode identity; nil =
	// not in AP mode (or --manage-network off).
	Hotspot func() *board.Hotspot
	// LastSTAError is the most recent failed WiFi-join error, preserved
	// across AP restore for the reconnecting provisioning user; "" = none.
	LastSTAError func() string
	// MDNSState reports the board's mDNS hostname (e.g.
	// "trainboard-ab12.local") when the responder is enabled; nil or ""
	// means the feature is off (--mdns=false). This is a static name, not
	// live per-interface state — per-interface add/remove detail is only in
	// the log (YAGNI).
	MDNSState func() string
	// UpdateStatus reports the M5 updater's state (available release,
	// rollback marker, last error). nil = updater not wired (dev mode);
	// reads as the zero Status, whose Enabled=false hides the controls.
	UpdateStatus func() update.Status
	// IPs reports local unicast IP addresses. Optional: nil falls back to
	// localIPs() (used in tests to override actual network state).
	IPs func() []string
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
	// WifiRetry asks the manager to attempt the configured WiFi now
	// (tears the AP down; the hotspot drops). No-op when nil.
	WifiRetry func()
	// NoteProvisioning marks live provisioning activity so the manager
	// suppresses its periodic retry. No-op when nil.
	NoteProvisioning func()
	// UpdateCheck / UpdateApply / UpdateDismiss drive M5 self-update.
	// UpdateApply stages the update WITHOUT restarting — the handler
	// renders its response then schedules the restart via Actions.Apply,
	// the same shape as config save. All three are nil when the updater
	// is not wired.
	UpdateCheck   func(ctx context.Context) error
	UpdateApply   func(ctx context.Context) error
	UpdateDismiss func() error
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
	// Update is the M5 updater's render-ready state; the zero value
	// (Enabled=false) hides the status page's Software section controls.
	Update update.Status
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
	st.Update = s.UpdateStatus()
	if s.src.IPs != nil {
		st.IPs = s.src.IPs()
	} else {
		st.IPs = localIPs()
	}
	st.IPs = dedupeStrings(st.IPs)
	return st
}

// dedupeStrings removes duplicate entries from in, preserving the order of
// first appearance. Used on the Address row's IP list: both Sources.IPs and
// localIPs() can report the same address twice (observed on-device:
// 10.55.0.1 via both the usb0 lifeline route and a general interface scan)
// (#70).
func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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

// ConfigRedacted loads the stored config with all secrets masked. It loads
// via config.LoadRaw (tolerant of a connectivity-valid-but-board-invalid
// document, e.g. a device that has only completed AP-mode partial setup):
// config.Load's internal Validate would otherwise error here, leaving the
// /config page unable to render at all for a partially-provisioned device
// that needs it most (to supply the missing origin/token). A genuinely
// unparseable/unreadable file still surfaces as an error, so the page can
// show it.
func (s *Service) ConfigRedacted() (config.Config, error) {
	c, err := config.LoadRaw(s.cfgPath)
	if err != nil {
		return config.Config{}, err
	}
	return c.Redacted(), nil
}

// ConfigUpdate carries a submitted config. Secret fields are write-only:
// empty means keep the stored value.
type ConfigUpdate struct {
	Cfg            config.Config
	NewToken       string
	NewWifiPSK     string
	NewPassword    string
	NewRTTPassword string
}

// UpdateConfig merges, validates, and transactionally saves. Nothing is
// written unless Validate passes.
//
// The initial read is config.LoadRaw, not config.Load: a device that only
// completed AP-mode partial setup has a connectivity-valid-but-board-invalid
// config on disk (password hash + WiFi, no origin/token), and config.Load's
// full Validate would reject it — blocking the very save that finishes
// provisioning. LoadRaw tolerates that document while still surfacing a
// genuinely unparseable/unreadable file as an error. The save side is
// unchanged: next.Validate() below still gates writes on the FULL tier, and
// config.Save re-validates, so an incomplete document can never be persisted
// as if it were a runnable board config.
func (s *Service) UpdateConfig(u ConfigUpdate) error {
	cur, err := config.LoadRaw(s.cfgPath)
	if err != nil {
		return err
	}
	next := cur
	next.Board = u.Cfg.Board
	next.Layout = u.Cfg.Layout
	next.Powersaving = u.Cfg.Powersaving
	next.Wifi.SSID = u.Cfg.Wifi.SSID
	next.Update = u.Cfg.Update
	next.RTT.Username = u.Cfg.RTT.Username
	if u.NewToken != "" {
		next.Darwin.Token = u.NewToken
	}
	if u.NewWifiPSK != "" {
		next.Wifi.PSK = u.NewWifiPSK
	}
	if u.NewRTTPassword != "" {
		next.RTT.Password = u.NewRTTPassword
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
	// config.LoadRaw parses without the full board Validate, so a
	// present-but-board-invalid file (e.g. an AP-mode partial-setup config
	// carrying a password hash + WiFi but no origin/token) still returns its
	// real stored fields — critical for the already-set guard below: with the
	// old config.Load read, such a file errored, the Default() fallback wiped
	// the stored hash from view, and this method would overwrite the password
	// (and drop the stored WiFi creds on save). Falling back to Default() on
	// a LoadRaw error remains deliberate: first-boot setup's whole job is
	// turning an invalid-or-absent document into a valid one, and a config
	// file this method can't even parse is treated the same as "no config
	// yet" — fresh setup over an unrecoverable file, rather than surfacing a
	// load error the operator can't act on from this form.
	cur, err := config.LoadRaw(s.cfgPath)
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

// SetupConnectivity completes the AP-mode partial setup flow: WiFi
// credentials plus the admin password, with no origin/token collected (that
// happens later, over LAN, at /config once the board has joined the
// network). It mirrors SetInitialPassword's shape — load-tolerant, refuse
// once a password already exists, hash the password — but targets the
// lighter ValidateConnectivity/SaveConnectivity tier instead of
// Validate/Save, since a device provisioned this way has no origin/token yet
// and is not expected to.
//
// ssid is required here (unlike config.validateWifi's "empty is fine" rule,
// which exists for the general config form where WiFi is optional): this
// method's whole purpose is joining WiFi, so a blank network name is
// rejected up front with a form-friendly message rather than falling through
// to validateWifi's "both fields must agree" checks. psk length (8-63) is
// still enforced by ValidateConnectivity's validateWifi call.
func (s *Service) SetupConnectivity(pw, ssid, psk string) error {
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if ssid == "" {
		return errors.New("wifi network name is required")
	}
	// See SetInitialPassword's doc comment: the tolerant config.LoadRaw read
	// keeps the already-set guard honest against a board-invalid document,
	// and an unparseable-or-absent config is treated the same as "no config
	// yet" for this load-tolerant setup flow, not surfaced as a load error.
	cur, err := config.LoadRaw(s.cfgPath)
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
	cur.Wifi.SSID = ssid
	cur.Wifi.PSK = psk
	if err := cur.ValidateConnectivity(); err != nil {
		return err
	}
	return config.SaveConnectivity(s.cfgPath, cur)
}

// NeedsSetup reports whether first-boot setup still needs to run, i.e. no
// admin password is stored yet. It loads via config.LoadRaw, not config.Load:
// a device that finished only AP-mode partial setup has a password hash + WiFi
// but no origin/token, so config.Load's full Validate would reject it and this
// would wrongly report setup-needed — re-arming the /setup gate and trapping
// the LAN user in a redirect loop away from /config. LoadRaw reads the stored
// hash regardless of board-validity: any LoadRaw error (unparseable/unreadable
// file) is still treated as "setup needed"; a successful read needs setup iff
// no password hash is stored.
func (s *Service) NeedsSetup() bool {
	cur, err := config.LoadRaw(s.cfgPath)
	if err != nil {
		return true
	}
	return cur.Web.PasswordHash == ""
}

// VerifyLogin checks a login attempt against the stored hash. It loads via
// config.LoadRaw for the same reason as NeedsSetup: an admin password set by
// AP-mode partial setup lives in a config that fails the full board Validate,
// and config.Load would refuse to return it — locking the operator out of the
// device they just provisioned. An unparseable/unreadable file (LoadRaw error)
// or an unset hash denies the login.
func (s *Service) VerifyLogin(pw string) bool {
	cur, err := config.LoadRaw(s.cfgPath)
	if err != nil || cur.Web.PasswordHash == "" {
		return false
	}
	return VerifyPassword(cur.Web.PasswordHash, pw)
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

// Hotspot reports the connectivity manager's AP-mode identity (nil = not in
// AP mode or --manage-network off). Nil-safe.
func (s *Service) Hotspot() *board.Hotspot {
	if s.src.Hotspot == nil {
		return nil
	}
	return s.src.Hotspot()
}

// LastSTAError reports the most recent failed WiFi-join error, preserved
// across AP restore for reconnection attempts by the provisioning user.
// Nil-safe; returns "" when no error is known.
func (s *Service) LastSTAError() string {
	if s.src.LastSTAError == nil {
		return ""
	}
	return s.src.LastSTAError()
}

// MDNSState reports the board's mDNS hostname when the responder is
// enabled ("" = off or --mdns=false). Nil-safe.
func (s *Service) MDNSState() string {
	if s.src.MDNSState == nil {
		return ""
	}
	return s.src.MDNSState()
}

// WifiRetryNow asks the connectivity manager to attempt the configured WiFi
// immediately (tears the AP down; the hotspot drops). Nil-safe no-op.
func (s *Service) WifiRetryNow() {
	if s.act.WifiRetry != nil {
		s.act.WifiRetry()
	}
}

// MarkProvisioning marks live provisioning activity so the manager suppresses
// its periodic retry. Nil-safe no-op.
func (s *Service) MarkProvisioning() {
	if s.act.NoteProvisioning != nil {
		s.act.NoteProvisioning()
	}
}

// UpdateStatus reports the updater's render-ready state. Nil-safe: an
// unwired seam reads as the zero Status (Enabled=false).
func (s *Service) UpdateStatus() update.Status {
	if s.src.UpdateStatus == nil {
		return update.Status{}
	}
	return s.src.UpdateStatus()
}

// CheckForUpdate runs an on-demand release check. Nil-safe.
func (s *Service) CheckForUpdate(ctx context.Context) error {
	if s.act.UpdateCheck == nil {
		return errors.New("updates are not available on this device")
	}
	return s.act.UpdateCheck(ctx)
}

// ApplyUpdate stages the available update into the inactive slot; the
// caller schedules the restart. Nil-safe.
func (s *Service) ApplyUpdate(ctx context.Context) error {
	if s.act.UpdateApply == nil {
		return errors.New("updates are not available on this device")
	}
	return s.act.UpdateApply(ctx)
}

// DismissRollback clears the rollback banner. Nil-safe.
func (s *Service) DismissRollback() error {
	if s.act.UpdateDismiss == nil {
		return nil
	}
	return s.act.UpdateDismiss()
}
