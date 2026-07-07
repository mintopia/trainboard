package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

func validCfg() config.Config {
	c := config.Default()
	c.Board.Origin = "PAD"
	c.Darwin.Token = "tok-original"
	return c
}

func newTestService(t *testing.T, cfg config.Config) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return newTestServiceAt(t, path), path
}

// newTestServiceAt wires a Service at an already-populated path, without
// touching the file (used by the virgin-device test, which writes an
// intentionally Validate()-failing fixture directly).
func newTestServiceAt(t *testing.T, path string) *Service {
	t.Helper()
	src := Sources{
		Snapshot:   func() *board.Snapshot { return nil },
		Ring:       obs.NewRing(8),
		PreviewPNG: func() []byte { return nil },
		Version:    "vtest",
		StartedAt:  time.Now().Add(-time.Hour),
	}
	return NewService(path, src, Actions{Apply: func() {}, Reboot: func() error { return nil }}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestStatusNilSnapshotIsInitialising(t *testing.T) {
	svc, _ := newTestService(t, validCfg())
	st := svc.Status()
	if st.State != "initialising" || st.HasSnapshot {
		t.Fatalf("status = %+v", st)
	}
	if st.Version != "vtest" || st.Uptime < time.Hour-time.Minute {
		t.Fatalf("version/uptime wrong: %+v", st)
	}
}

func TestStatusEventsNewestFirstCapped(t *testing.T) {
	svc, _ := newTestService(t, validCfg())
	for i := 0; i < 8; i++ {
		svc.src.Ring.Add(obs.Event{Msg: string(rune('a' + i))})
	}
	ev := svc.Status().Events
	if len(ev) != 8 || ev[0].Msg != "h" || ev[7].Msg != "a" {
		t.Fatalf("events not newest-first: %+v", ev)
	}
}

// TestStatusEventsCapAt50 exercises maxStatusEvents itself: a ring holding
// more than 50 events must still only surface the newest 50, newest first.
func TestStatusEventsCapAt50(t *testing.T) {
	src := Sources{
		Snapshot:   func() *board.Snapshot { return nil },
		Ring:       obs.NewRing(64),
		PreviewPNG: func() []byte { return nil },
		Version:    "vtest",
		StartedAt:  time.Now(),
	}
	svc := NewService(filepath.Join(t.TempDir(), "config.json"), src, Actions{Apply: func() {}, Reboot: func() error { return nil }}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := config.Save(svc.cfgPath, validCfg()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		src.Ring.Add(obs.Event{Msg: fmt.Sprintf("evt-%d", i)})
	}
	ev := svc.Status().Events
	if len(ev) != maxStatusEvents {
		t.Fatalf("events len = %d, want %d", len(ev), maxStatusEvents)
	}
	if ev[0].Msg != "evt-59" {
		t.Fatalf("newest event first: got %q", ev[0].Msg)
	}
	if ev[len(ev)-1].Msg != "evt-10" {
		t.Fatalf("oldest surfaced event should be evt-10 (60 pushed, cap 50): got %q", ev[len(ev)-1].Msg)
	}
}

func TestConfigRedactedNeverReturnsSecrets(t *testing.T) {
	cfg := validCfg()
	cfg.Wifi = config.WifiConfig{SSID: "net", PSK: "wifisecret"}
	svc, _ := newTestService(t, cfg)
	got, err := svc.ConfigRedacted()
	if err != nil {
		t.Fatal(err)
	}
	if got.Darwin.Token == "tok-original" || got.Wifi.PSK == "wifisecret" {
		t.Fatalf("secrets leaked: %+v", got)
	}
	if got.Board.Origin != "PAD" || got.Wifi.SSID != "net" {
		t.Fatalf("non-secrets mangled: %+v", got)
	}
}

func TestUpdateConfigWriteOnlySecrets(t *testing.T) {
	svc, path := newTestService(t, validCfg())
	u := ConfigUpdate{Cfg: validCfg()}
	u.Cfg.Board.RefreshSeconds = 120
	// all secret fields blank: keep originals
	if err := svc.UpdateConfig(u); err != nil {
		t.Fatal(err)
	}
	stored, _ := config.Load(path)
	if stored.Darwin.Token != "tok-original" {
		t.Fatal("blank token must keep stored value")
	}
	if stored.Board.RefreshSeconds != 120 {
		t.Fatal("non-secret change must persist")
	}
	// set a new token
	u.NewToken = "tok-new"
	if err := svc.UpdateConfig(u); err != nil {
		t.Fatal(err)
	}
	stored, _ = config.Load(path)
	if stored.Darwin.Token != "tok-new" {
		t.Fatal("new token must replace stored value")
	}
}

func TestUpdateConfigRejectsInvalid(t *testing.T) {
	svc, path := newTestService(t, validCfg())
	u := ConfigUpdate{Cfg: validCfg()}
	u.Cfg.Board.RefreshSeconds = 5 // below minimum
	if err := svc.UpdateConfig(u); err == nil {
		t.Fatal("invalid config must be rejected")
	}
	stored, _ := config.Load(path)
	if stored.Board.RefreshSeconds == 5 {
		t.Fatal("rejected config must not be written")
	}
}

// TestUpdateConfigRejectsShortPassword covers the finding this task resolves:
// UpdateConfig must enforce the same 8-character minimum as
// SetInitialPassword when NewPassword is set. Nothing must be written on
// rejection, including the stored password hash.
func TestUpdateConfigRejectsShortPassword(t *testing.T) {
	svc, path := newTestService(t, validCfg())
	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	u := ConfigUpdate{Cfg: validCfg(), NewPassword: "short"}
	if err := svc.UpdateConfig(u); err == nil {
		t.Fatal("password shorter than 8 characters must be rejected")
	} else if !strings.Contains(err.Error(), "at least 8 characters") {
		t.Fatalf("error must mention the 8-character minimum, got: %v", err)
	}

	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected password change must write nothing:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestSetInitialPasswordOnlyOnce(t *testing.T) {
	svc, path := newTestService(t, validCfg())
	if err := svc.SetInitialPassword("short", "PAD", ""); err == nil {
		t.Fatal("short password must be rejected")
	}
	if err := svc.SetInitialPassword("longenough1", "PAD", ""); err != nil {
		t.Fatal(err)
	}
	stored, _ := config.Load(path)
	if !VerifyPassword(stored.Web.PasswordHash, "longenough1") {
		t.Fatal("stored hash must verify")
	}
	if err := svc.SetInitialPassword("another-pass", "PAD", ""); err == nil {
		t.Fatal("second setup must be rejected once a password exists")
	}
}

// TestSetInitialPasswordVirginDevice covers the caveat this task resolved:
// config.Save/Load both validate, and Default() (empty origin, empty token)
// fails Validate. A virgin device therefore has no config.Save-produced file
// on disk yet — first-boot setup must supply origin (+ optional token)
// alongside the password so the resulting document is valid. We write
// Default() directly with os.WriteFile to model that on-disk state, bypassing
// Save's validation deliberately for this fixture.
func TestSetInitialPasswordVirginDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw, err := json.Marshal(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newTestServiceAt(t, path)

	if err := svc.SetInitialPassword("longenough1", "PAD", "tok-first"); err != nil {
		t.Fatalf("first-boot setup on a virgin config must succeed: %v", err)
	}
	stored, err := config.Load(path)
	if err != nil {
		t.Fatalf("stored config must now validate: %v", err)
	}
	if stored.Board.Origin != "PAD" {
		t.Fatalf("origin not persisted: %+v", stored)
	}
	if stored.Darwin.Token != "tok-first" {
		t.Fatalf("token not persisted: %+v", stored)
	}
	if !VerifyPassword(stored.Web.PasswordHash, "longenough1") {
		t.Fatal("stored hash must verify")
	}
}

// TestSetInitialPasswordVirginDeviceBlankTokenRejected covers the finding
// this task resolves: a genuinely virgin device (no config file at all) has
// no stored Darwin token to fall back on, so a blank token at setup must be
// rejected by cur.Validate() — it is not a valid way to "configure the token
// later." No config file must be written when this happens.
func TestSetInitialPasswordVirginDeviceBlankTokenRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	svc := newTestServiceAt(t, path) // no file at path: config.Load falls back to Default()

	err := svc.SetInitialPassword("longenough1", "PAD", "")
	if err == nil {
		t.Fatal("blank token on a virgin device must be rejected")
	}
	if !strings.Contains(err.Error(), "darwin.token") {
		t.Fatalf("error must mention darwin.token, got: %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("no config file must be created on rejection, stat err = %v", statErr)
	}
}

// TestSetInitialPasswordBlankTokenKeepsStoredToken pins the companion case:
// a device with an already-valid stored config (token present, no password
// hash yet — e.g. provisioned by an installer) can complete first-boot setup
// with a blank token, and the stored token is left untouched.
func TestSetInitialPasswordBlankTokenKeepsStoredToken(t *testing.T) {
	svc, path := newTestService(t, validCfg()) // validCfg has Darwin.Token = "tok-original"

	if err := svc.SetInitialPassword("longenough1", "PAD", ""); err != nil {
		t.Fatalf("blank token must be accepted when a token is already stored: %v", err)
	}
	stored, err := config.Load(path)
	if err != nil {
		t.Fatalf("stored config must validate: %v", err)
	}
	if stored.Darwin.Token != "tok-original" {
		t.Fatalf("blank token at setup must keep the stored token, got %q", stored.Darwin.Token)
	}
	if !VerifyPassword(stored.Web.PasswordHash, "longenough1") {
		t.Fatal("stored hash must verify")
	}
}

// TestNeedsSetup covers the three states NeedsSetup must distinguish: a
// virgin directory (no config file at all — config.Load's missing-file
// fallback to Default() has an empty PasswordHash), a valid saved config with
// no password hash yet, and a valid saved config with one.
func TestNeedsSetup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	svc := newTestServiceAt(t, path)
	if !svc.NeedsSetup() {
		t.Fatal("virgin directory (no config file) must need setup")
	}

	cfg := validCfg()
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if !svc.NeedsSetup() {
		t.Fatal("valid config without a password hash must need setup")
	}

	h, err := HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.PasswordHash = h
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if svc.NeedsSetup() {
		t.Fatal("config with a password hash must not need setup")
	}
}

func TestVerifyLogin(t *testing.T) {
	cfg := validCfg()
	h, _ := HashPassword("hunter22")
	cfg.Web.PasswordHash = h
	svc, _ := newTestService(t, cfg)
	if !svc.VerifyLogin("hunter22") || svc.VerifyLogin("wrong") {
		t.Fatal("login verification wrong")
	}
	svcNoPw, _ := newTestService(t, validCfg())
	if svcNoPw.VerifyLogin("anything") {
		t.Fatal("no stored hash must never verify")
	}
}

func TestRegenerateAPPassword(t *testing.T) {
	svc, path := newTestService(t, validCfg())
	pw, err := svc.RegenerateAPPassword()
	if err != nil || len(pw) != 12 {
		t.Fatalf("pw=%q err=%v", pw, err)
	}
	if strings.ContainsAny(pw, "01lioLIO") {
		t.Fatalf("ambiguous characters in %q", pw)
	}
	stored, _ := config.Load(path)
	if stored.Provisioning.APPassword != pw {
		t.Fatal("AP password must persist")
	}
	pw2, _ := svc.RegenerateAPPassword()
	if pw2 == pw {
		t.Fatal("regeneration must change the password")
	}
}

func TestServiceStartSoakValidatesDuration(t *testing.T) {
	_, svc, _, _ := newConfigTestServer(t)

	if err := svc.StartSoak("2h"); err == nil {
		t.Fatal("StartSoak(\"2h\") = nil error, want invalid-duration error")
	}
	if err := svc.StartSoak(""); err == nil {
		t.Fatal("StartSoak(\"\") = nil error, want invalid-duration error")
	}
	if err := svc.StartSoak("8h"); err != nil {
		t.Fatalf("StartSoak(\"8h\") = %v, want nil", err)
	}
	if got := svc.SoakRemaining(); got != 8*time.Hour {
		t.Fatalf("SoakRemaining = %v, want 8h (harness fake stores the started duration)", got)
	}
	svc.CancelSoak()
	if got := svc.SoakRemaining(); got != 0 {
		t.Fatalf("after CancelSoak: SoakRemaining = %v, want 0", got)
	}
}

func TestServiceStatusCarriesSoakRemaining(t *testing.T) {
	_, svc, _, _ := newConfigTestServer(t)
	if err := svc.StartSoak("1h"); err != nil {
		t.Fatal(err)
	}
	if got := svc.Status().SoakRemaining; got != time.Hour {
		t.Fatalf("Status().SoakRemaining = %v, want 1h", got)
	}
}

func TestServiceSoakNilSourcesSafe(t *testing.T) {
	// A Service whose Sources/Actions omit the soak funcs (older callers,
	// other tests) must not panic: reads report 0, StartSoak errors.
	// Snapshot/Ring/PreviewPNG must still be wired — Status() dereferences
	// them unconditionally; only the soak funcs are deliberately absent.
	src := Sources{
		Snapshot:   func() *board.Snapshot { return nil },
		Ring:       obs.NewRing(1),
		PreviewPNG: func() []byte { return nil },
		StartedAt:  time.Now(),
	}
	svc := NewService("/nonexistent", src, Actions{}, testLog())
	if got := svc.SoakRemaining(); got != 0 {
		t.Fatalf("SoakRemaining = %v, want 0", got)
	}
	if got := svc.Status().SoakRemaining; got != 0 {
		t.Fatalf("Status().SoakRemaining = %v, want 0", got)
	}
	if err := svc.StartSoak("1h"); err == nil {
		t.Fatal("StartSoak with no Actions.SoakStart wired: want error, got nil")
	}
	svc.CancelSoak() // must not panic
}

func TestServiceConnectivitySeams(t *testing.T) {
	srv, svc, _, _, conn := newConnTestServer(t)
	_ = srv
	if got := svc.Hotspot(); got != nil {
		t.Fatalf("no AP mode: Hotspot() = %v, want nil", got)
	}
	conn.set(&board.Hotspot{SSID: "Trainboard-AB12", Password: "pw", Addr: "192.168.4.1"}, "join failed: wrong PSK")
	if got := svc.Hotspot(); got == nil || got.SSID != "Trainboard-AB12" {
		t.Fatalf("Hotspot() = %v", got)
	}
	if got := svc.LastSTAError(); got != "join failed: wrong PSK" {
		t.Fatalf("LastSTAError() = %q", got)
	}
	svc.WifiRetryNow()
	svc.MarkProvisioning()
	if r, p := conn.counts(); r != 1 || p != 1 {
		t.Fatalf("retry/prov counts = %d/%d, want 1/1", r, p)
	}
}

func TestServiceConnectivityNilSeamsSafe(t *testing.T) {
	src := Sources{Snapshot: func() *board.Snapshot { return nil }, Ring: obs.NewRing(1),
		PreviewPNG: func() []byte { return nil }, StartedAt: time.Now()}
	svc := NewService("/nonexistent", src, Actions{}, testLog())
	if svc.Hotspot() != nil || svc.LastSTAError() != "" {
		t.Fatal("nil seams must read as inactive")
	}
	svc.WifiRetryNow()      // must not panic
	svc.MarkProvisioning()  // must not panic
}
