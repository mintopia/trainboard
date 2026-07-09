package update

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	minisign "github.com/jedisct1/go-minisign"
)

// maxBinarySize caps the decompressed payload (decompression-bomb guard;
// the real binary is ~15 MB).
const maxBinarySize = 100 << 20 // 100 MiB

// Applier downloads, verifies, and installs a release into the inactive
// slot (spec §2 apply flow). It never writes the known-good slot.
type Applier struct {
	SlotsDir  string // e.g. /opt/trainboard/slots
	StatePath string
	Running   string // buildinfo.Version() of this process
	Keys      []minisign.PublicKey
	HTTP      *http.Client
	Log       *slog.Logger
}

// otherSlot maps a↔b.
func otherSlot(s string) string {
	if s == "a" {
		return "b"
	}
	return "a"
}

// Apply runs the full pipeline for rel:
//
//  1. fetch manifest.json + manifest.json.minisig; verify the signature
//     against the keyring BEFORE parsing anything;
//  2. CheckInstallable (arch / strictly-newer / floor — spec §1);
//  3. stream the gzipped asset into a temp file in the TARGET slot's own
//     directory (same filesystem ⇒ atomic rename), hashing the
//     decompressed bytes as they land;
//  4. compare sha256, chmod 0755, fsync file + directory, rename;
//  5. flip state: Active=target, attempts reset, floor ratcheted;
//     KnownGood untouched.
//
// The target is always otherSlot(KnownGood) — NOT otherSlot(Active) — so a
// re-apply while an unpromoted update is already pending overwrites the
// pending slot rather than the known-good one (double-fault guarantee).
//
// Every failure path leaves the state document and the known-good slot
// exactly as they were: update failures are non-fatal by design (spec §3).
func (a *Applier) Apply(ctx context.Context, rel *Release) error {
	manURL, ok := rel.AssetURL("manifest.json")
	if !ok {
		return fmt.Errorf("update: release %s has no manifest.json asset", rel.Version)
	}
	sigURL, ok := rel.AssetURL("manifest.json.minisig")
	if !ok {
		return fmt.Errorf("update: release %s has no manifest.json.minisig asset", rel.Version)
	}
	manRaw, err := a.fetchSmall(ctx, manURL)
	if err != nil {
		return err
	}
	sigRaw, err := a.fetchSmall(ctx, sigURL)
	if err != nil {
		return err
	}
	if err := VerifyManifest(a.Keys, manRaw, sigRaw); err != nil {
		return err
	}
	m, err := ParseManifest(manRaw)
	if err != nil {
		return err
	}

	st, err := LoadState(a.StatePath)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("update: no updater state — this is not a slot install (run the migration first)")
	}
	if err != nil {
		return err
	}
	if err := m.CheckInstallable(a.Running, st.VersionFloor); err != nil {
		return err
	}

	target := otherSlot(st.KnownGood)
	assetURL, ok := rel.AssetURL(m.Asset)
	if !ok {
		return fmt.Errorf("update: release %s has no asset %q named by its manifest", rel.Version, m.Asset)
	}
	a.Log.Info("applying update", "version", m.Version, "slot", target, "asset", m.Asset)
	if err := a.installBinary(ctx, assetURL, m.SHA256, target); err != nil {
		return err
	}

	st.Active = target
	st.ActiveVersion = m.Version
	st.BootAttempts = 0
	st.VersionFloor = maxVersion(st.VersionFloor, m.MinVersion)
	if err := SaveState(a.StatePath, st); err != nil {
		return err
	}
	a.Log.Info("update staged; restart to boot it", "version", m.Version, "slot", target)
	return nil
}

// installBinary streams the gzipped asset at url into
// SlotsDir/<slot>/trainboard via a same-directory temp file, verifying the
// decompressed sha256 before the rename makes it visible.
func (a *Applier) installBinary(ctx context.Context, url, wantSHA, slot string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("update: building download request: %w", err)
	}
	req.Header.Set("User-Agent", "trainboard-updater")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("update: downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update: downloading asset: %s", resp.Status)
	}

	slotDir := filepath.Join(a.SlotsDir, slot)
	if err := os.MkdirAll(slotDir, 0o755); err != nil {
		return fmt.Errorf("update: creating slot dir: %w", err)
	}
	tmp, err := os.CreateTemp(slotDir, ".trainboard-*.tmp")
	if err != nil {
		return fmt.Errorf("update: creating temp binary: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: asset is not gzip: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(gz, maxBinarySize+1))
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: decompressing asset: %w", err)
	}
	if n > maxBinarySize {
		_ = tmp.Close()
		return fmt.Errorf("update: decompressed asset exceeds %d bytes", maxBinarySize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(wantSHA) {
		_ = tmp.Close()
		return fmt.Errorf("update: sha256 mismatch: manifest %s, downloaded %s", wantSHA, got)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: chmod binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: fsync binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("update: closing binary: %w", err)
	}
	final := filepath.Join(slotDir, "trainboard")
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("update: renaming binary into place: %w", err)
	}
	// fsync the directory so the rename itself survives power loss.
	if d, err := os.Open(slotDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// fetchSmall GETs a small artifact (manifest / signature) with a 1 MiB cap.
func (a *Applier) fetchSmall(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: building request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "trainboard-updater")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: fetching %s: %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return nil, fmt.Errorf("update: reading %s: %w", url, err)
	}
	return raw, nil
}
