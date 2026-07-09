package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/update"
)

func TestMkManifest(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "trainboard")
	payload := []byte("fake binary contents")
	if err := os.WriteFile(bin, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := mkmanifest(&out, args{
		version: "v0.2.0", channel: "stable", commit: "abc1234",
		asset: "trainboard_v0.2.0_linux_arm64.gz", binary: bin, minVersion: "v0.1.0",
	})
	if err != nil {
		t.Fatalf("mkmanifest: %v", err)
	}
	var m update.Manifest
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("output not valid manifest JSON: %v", err)
	}
	sum := sha256.Sum256(payload)
	want := update.Manifest{
		Version: "v0.2.0", Channel: "stable", Commit: "abc1234", Arch: update.RequiredArch,
		Asset: "trainboard_v0.2.0_linux_arm64.gz", SHA256: hex.EncodeToString(sum[:]), MinVersion: "v0.1.0",
	}
	if m != want {
		t.Errorf("manifest:\n got %+v\nwant %+v", m, want)
	}
}

func TestMkManifestValidation(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "trainboard")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := args{version: "v0.2.0", channel: "stable", commit: "abc1234",
		asset: "a.gz", binary: bin, minVersion: "v0.1.0"}

	bad := base
	bad.version = "0.2.0" // missing v prefix
	if err := mkmanifest(&bytes.Buffer{}, bad); err == nil {
		t.Error("invalid semver accepted")
	}
	bad = base
	bad.channel = "nightly"
	if err := mkmanifest(&bytes.Buffer{}, bad); err == nil {
		t.Error("invalid channel accepted")
	}
	bad = base
	bad.binary = filepath.Join(t.TempDir(), "missing")
	if err := mkmanifest(&bytes.Buffer{}, bad); err == nil {
		t.Error("missing binary accepted")
	}
}
