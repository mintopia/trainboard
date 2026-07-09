package update

import (
	"strings"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		Version: "v0.2.0", Channel: "stable", Commit: "abc1234",
		Arch: "linux/arm64", Asset: "trainboard_v0.2.0_linux_arm64.gz",
		SHA256: strings.Repeat("ab", 32), MinVersion: "v0.1.0",
	}
}

func TestParseManifest(t *testing.T) {
	raw := []byte(`{"version":"v0.2.0","channel":"stable","commit":"abc1234",` +
		`"arch":"linux/arm64","asset":"trainboard_v0.2.0_linux_arm64.gz",` +
		`"sha256":"` + strings.Repeat("ab", 32) + `","min_version":"v0.1.0"}`)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m != validManifest() {
		t.Errorf("got %+v, want %+v", m, validManifest())
	}
	if _, err := ParseManifest([]byte("{nope")); err == nil {
		t.Error("garbage JSON accepted")
	}
}

func TestCheckInstallable(t *testing.T) {
	mod := func(f func(*Manifest)) Manifest { m := validManifest(); f(&m); return m }
	tests := []struct {
		name           string
		m              Manifest
		running, floor string
		wantErr        string // substring; "" = installable
	}{
		{name: "upgrade from older release", m: validManifest(), running: "v0.1.0", floor: "v0.1.0"},
		{name: "dev build always upgradeable", m: validManifest(), running: "dev", floor: ""},
		{name: "empty floor ok", m: validManifest(), running: "v0.1.0", floor: ""},
		{name: "same version rejected", m: validManifest(), running: "v0.2.0", floor: "", wantErr: "not newer"},
		{name: "downgrade rejected", m: validManifest(), running: "v0.3.0", floor: "", wantErr: "not newer"},
		{name: "replayed manifest below floor rejected",
			m: validManifest(), running: "dev", floor: "v0.5.0", wantErr: "version floor"},
		{name: "wrong arch rejected",
			m: mod(func(m *Manifest) { m.Arch = "linux/amd64" }), running: "v0.1.0", wantErr: "arch"},
		{name: "invalid semver version rejected",
			m: mod(func(m *Manifest) { m.Version = "banana" }), running: "v0.1.0", wantErr: "semver"},
		{name: "invalid min_version rejected",
			m: mod(func(m *Manifest) { m.MinVersion = "banana" }), running: "v0.1.0", wantErr: "min_version"},
		{name: "missing asset rejected",
			m: mod(func(m *Manifest) { m.Asset = "" }), running: "v0.1.0", wantErr: "asset"},
		{name: "missing sha256 rejected",
			m: mod(func(m *Manifest) { m.SHA256 = "" }), running: "v0.1.0", wantErr: "sha256"},
		{name: "prerelease ordering: v0.2.0-rc1 not newer than v0.2.0",
			m: mod(func(m *Manifest) { m.Version = "v0.2.0-rc1" }), running: "v0.2.0", wantErr: "not newer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.m.CheckInstallable(tt.running, tt.floor)
			if tt.wantErr == "" && err != nil {
				t.Errorf("CheckInstallable: %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("CheckInstallable = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestMaxVersion(t *testing.T) {
	tests := []struct{ a, b, want string }{
		{"v0.1.0", "v0.2.0", "v0.2.0"},
		{"v0.2.0", "v0.1.0", "v0.2.0"},
		{"", "v0.1.0", "v0.1.0"},
		{"v0.1.0", "", "v0.1.0"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := maxVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("maxVersion(%q,%q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}
