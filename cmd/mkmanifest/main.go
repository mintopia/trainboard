// Command mkmanifest emits the signed-release manifest JSON (M5 spec §1)
// for a built binary. CI runs it between build and minisign; it reuses
// update.Manifest so the generator and the on-device verifier can never
// drift on schema.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/mod/semver"

	"github.com/mintopia/trainboard/internal/update"
)

type args struct {
	version, channel, commit, asset, binary, minVersion string
}

func main() {
	var a args
	flag.StringVar(&a.version, "version", "", "release version (vX.Y.Z)")
	flag.StringVar(&a.channel, "channel", "stable", "stable or prerelease")
	flag.StringVar(&a.commit, "commit", "", "source commit (short sha)")
	flag.StringVar(&a.asset, "asset", "", "gzipped binary asset filename")
	flag.StringVar(&a.binary, "binary", "", "path to the DECOMPRESSED binary to hash")
	flag.StringVar(&a.minVersion, "min-version", "", "minimum-rollback version floor")
	flag.Parse()
	if err := mkmanifest(os.Stdout, a); err != nil {
		fmt.Fprintln(os.Stderr, "mkmanifest:", err)
		os.Exit(1)
	}
}

func mkmanifest(w io.Writer, a args) error {
	if !semver.IsValid(a.version) {
		return fmt.Errorf("version %q is not valid semver (vX.Y.Z)", a.version)
	}
	if a.channel != "stable" && a.channel != "prerelease" {
		return fmt.Errorf("channel %q must be stable or prerelease", a.channel)
	}
	if a.minVersion != "" && !semver.IsValid(a.minVersion) {
		return fmt.Errorf("min-version %q is not valid semver", a.minVersion)
	}
	if a.asset == "" || a.commit == "" {
		return fmt.Errorf("asset and commit are required")
	}
	f, err := os.Open(a.binary)
	if err != nil {
		return fmt.Errorf("opening binary: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hashing binary: %w", err)
	}
	m := update.Manifest{
		Version: a.version, Channel: a.channel, Commit: a.commit,
		Arch: update.RequiredArch, Asset: a.asset,
		SHA256: hex.EncodeToString(h.Sum(nil)), MinVersion: a.minVersion,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}
