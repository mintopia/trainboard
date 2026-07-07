package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadMissingReturnsDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != CurrentVersion || c.Board.Services != 3 {
		t.Fatalf("missing-file load not default: %+v", c)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Darwin.Token = "GUID"
	c.Board.Origin = "PAD"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Board.Origin != "PAD" || back.Darwin.Token != "GUID" {
		t.Fatalf("round-trip lost data: %+v", back)
	}
}

func TestSaveUsesMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Darwin.Token = "GUID"
	c.Board.Origin = "PAD"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default() // no token, no origin ⇒ invalid
	if err := Save(path, c); err == nil {
		t.Fatal("expected Save to reject invalid config")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid Save must not create the file")
	}
}

func TestLoadRejectsInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"board":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected Load to reject an invalid config file")
	}
}

func TestSaveConnectivityThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Web.PasswordHash = "$argon2id$fake"
	c.Provisioning.APPassword = "genpw123456"
	if err := SaveConnectivity(path, c); err != nil {
		t.Fatal(err)
	}
	back, err := LoadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Web.PasswordHash != "$argon2id$fake" || back.Provisioning.APPassword != "genpw123456" {
		t.Fatalf("round-trip lost data: %+v", back)
	}
}

func TestSaveConnectivityRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default() // no web.passwordHash ⇒ fails ValidateConnectivity
	if err := SaveConnectivity(path, c); err == nil {
		t.Fatal("expected SaveConnectivity to reject a connectivity-invalid config")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid SaveConnectivity must not create the file")
	}
}

func TestSaveConnectivityUsesMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Web.PasswordHash = "$argon2id$fake"
	if err := SaveConnectivity(path, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSaveConnectivityNoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	c := Default()
	c.Web.PasswordHash = "$argon2id$fake"
	if err := SaveConnectivity(path, c); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected only config.json, found %d entries", len(entries))
	}
}

func TestLoadRawMissingReturnsDefault(t *testing.T) {
	c, err := LoadRaw(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != CurrentVersion || c.Board.Services != 3 {
		t.Fatalf("missing-file LoadRaw not default: %+v", c)
	}
}

func TestLoadRawPreservesProvisioningOnBoardInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// board.origin is missing (fails Validate), but provisioning/web are
	// populated as they would be on a previously-configured device.
	body := `{"version":1,"provisioning":{"apPassword":"persisted-pw"},"web":{"passwordHash":"$argon2id$fake"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected Load to reject a board-invalid config file")
	}

	c, err := LoadRaw(path)
	if err != nil {
		t.Fatalf("LoadRaw should tolerate a board-invalid document: %v", err)
	}
	if c.Provisioning.APPassword != "persisted-pw" {
		t.Fatalf("LoadRaw lost Provisioning.APPassword: %+v", c.Provisioning)
	}
	if c.Web.PasswordHash != "$argon2id$fake" {
		t.Fatalf("LoadRaw lost Web.PasswordHash: %+v", c.Web)
	}
}

func TestLoadRawUnparsableReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRaw(path); err == nil {
		t.Fatal("expected LoadRaw to reject unparsable JSON")
	}
}

func TestSaveNoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	c := Default()
	c.Darwin.Token = "GUID"
	c.Board.Origin = "PAD"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected only config.json, found %d entries", len(entries))
	}
}
