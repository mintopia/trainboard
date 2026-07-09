package update

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applyFixture wires a complete fake release: binary, signed manifest,
// download server, seeded state, and a ready Applier.
type applyFixture struct {
	applier *Applier
	release *Release
	binary  []byte
	state   string // state path
}

func newApplyFixture(t *testing.T, mutate func(*Manifest), seed State) *applyFixture {
	t.Helper()
	binary := []byte("#!/fake-arm64-binary v0.2.0\n" + strings.Repeat("x", 4096))
	sum := sha256.Sum256(binary)

	m := Manifest{
		Version: "v0.2.0", Channel: "stable", Commit: "abc1234", Arch: RequiredArch,
		Asset: "trainboard_v0.2.0_linux_arm64.gz", SHA256: hex.EncodeToString(sum[:]),
		MinVersion: "v0.1.0",
	}
	if mutate != nil {
		mutate(&m)
	}
	manRaw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	pubLine, sign := testKeypair(t, "AAAAAAAA")
	keys, err := ParsePublicKeys([]string{pubLine})
	if err != nil {
		t.Fatal(err)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(manRaw) })
	mux.HandleFunc("/manifest.json.minisig", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sign(manRaw)) })
	mux.HandleFunc("/bin.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(gz.Bytes()) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := SaveState(statePath, seed); err != nil {
		t.Fatal(err)
	}

	return &applyFixture{
		applier: &Applier{
			SlotsDir: filepath.Join(dir, "slots"), StatePath: statePath,
			Running: "v0.1.0", Keys: keys, HTTP: srv.Client(), Log: testLogger(),
		},
		release: &Release{Version: "v0.2.0", Assets: []Asset{
			{Name: "manifest.json", URL: srv.URL + "/manifest.json"},
			{Name: "manifest.json.minisig", URL: srv.URL + "/manifest.json.minisig"},
			{Name: "trainboard_v0.2.0_linux_arm64.gz", URL: srv.URL + "/bin.gz"},
		}},
		binary: binary,
		state:  statePath,
	}
}

func goodSeed() State {
	return State{Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", VersionFloor: "v0.1.0"}
}

func TestApplyHappyPath(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	if err := f.applier.Apply(context.Background(), f.release); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Binary landed in the INACTIVE slot (b), decompressed, executable.
	got, err := os.ReadFile(filepath.Join(f.applier.SlotsDir, "b", "trainboard"))
	if err != nil {
		t.Fatalf("installed binary: %v", err)
	}
	if !bytes.Equal(got, f.binary) {
		t.Error("installed binary differs from source")
	}
	info, err := os.Stat(filepath.Join(f.applier.SlotsDir, "b", "trainboard"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary not executable: %v", info.Mode())
	}
	// State flipped: b pending, a still known-good, attempts reset,
	// floor ratcheted.
	st, err := LoadState(f.state)
	if err != nil {
		t.Fatal(err)
	}
	want := State{
		Active: "b", ActiveVersion: "v0.2.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 0, VersionFloor: "v0.1.0",
	}
	if st != want {
		t.Errorf("state after apply:\n got %+v\nwant %+v", st, want)
	}
}

func TestApplyNeverWritesKnownGoodSlot(t *testing.T) {
	// Pending unpromoted update already in b (Active=b, KnownGood=a):
	// applying again must overwrite the PENDING slot b, never a.
	seed := State{Active: "b", ActiveVersion: "v0.1.5", KnownGood: "a", KnownGoodVersion: "v0.1.0"}
	f := newApplyFixture(t, nil, seed)
	knownGoodBin := filepath.Join(f.applier.SlotsDir, "a", "trainboard")
	if err := os.MkdirAll(filepath.Dir(knownGoodBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownGoodBin, []byte("known-good, do not touch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.applier.Apply(context.Background(), f.release); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(knownGoodBin)
	if err != nil || string(got) != "known-good, do not touch" {
		t.Fatalf("KNOWN-GOOD SLOT WAS WRITTEN (double-fault guarantee violated): %q, %v", got, err)
	}
	st, _ := LoadState(f.state)
	if st.Active != "b" || st.ActiveVersion != "v0.2.0" || st.KnownGood != "a" {
		t.Errorf("state after re-apply: %+v", st)
	}
}

func TestApplyRejectsBadSHA(t *testing.T) {
	f := newApplyFixture(t, func(m *Manifest) { m.SHA256 = strings.Repeat("00", 32) }, goodSeed())
	if err := f.applier.Apply(context.Background(), f.release); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("bad sha256: got %v", err)
	}
	// Nothing installed, state untouched.
	if _, err := os.Stat(filepath.Join(f.applier.SlotsDir, "b", "trainboard")); err == nil {
		t.Error("binary installed despite sha mismatch")
	}
	st, _ := LoadState(f.state)
	if st != goodSeed() {
		t.Errorf("state mutated on failed apply: %+v", st)
	}
}

func TestApplyRejectsUnsignedManifest(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	// Replace the signature with one from an untrusted key.
	_, strangerSign := testKeypair(t, "ZZZZZZZZ")
	manURL, _ := f.release.AssetURL("manifest.json")
	resp, err := f.applier.HTTP.Get(manURL)
	if err != nil {
		t.Fatal(err)
	}
	manRaw := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		manRaw = append(manRaw, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	_ = resp.Body.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(strangerSign(manRaw))
	}))
	t.Cleanup(srv.Close)
	for i, a := range f.release.Assets {
		if a.Name == "manifest.json.minisig" {
			f.release.Assets[i].URL = srv.URL
		}
	}
	if err := f.applier.Apply(context.Background(), f.release); err == nil || !strings.Contains(err.Error(), "trusted") {
		t.Errorf("untrusted signature: got %v", err)
	}
}

func TestApplyRejectsDowngradeAndFloor(t *testing.T) {
	// Running version already newer than the release.
	f := newApplyFixture(t, nil, goodSeed())
	f.applier.Running = "v0.3.0"
	if err := f.applier.Apply(context.Background(), f.release); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Errorf("downgrade: got %v", err)
	}
	// Replay below the persisted floor.
	seed := goodSeed()
	seed.VersionFloor = "v0.5.0"
	f2 := newApplyFixture(t, nil, seed)
	f2.applier.Running = "dev"
	if err := f2.applier.Apply(context.Background(), f2.release); err == nil || !strings.Contains(err.Error(), "floor") {
		t.Errorf("floor replay: got %v", err)
	}
}

func TestApplyRequiresStateFile(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	if err := os.Remove(f.state); err != nil {
		t.Fatal(err)
	}
	if err := f.applier.Apply(context.Background(), f.release); err == nil {
		t.Error("apply without updater state (not a slot install) must fail")
	}
}

func TestApplyTruncatedDownload(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "999999")
		_, _ = w.Write([]byte("\x1f\x8b\x08\x00trunc")) // gzip magic then garbage/EOF
	}))
	t.Cleanup(srv.Close)
	for i, a := range f.release.Assets {
		if a.Name == "trainboard_v0.2.0_linux_arm64.gz" {
			f.release.Assets[i].URL = srv.URL
		}
	}
	if err := f.applier.Apply(context.Background(), f.release); err == nil {
		t.Error("truncated download accepted")
	}
	st, _ := LoadState(f.state)
	if st != goodSeed() {
		t.Errorf("state mutated on failed apply: %+v", st)
	}
}

func TestApplySweepsStaleTempFiles(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	targetSlotDir := filepath.Join(f.applier.SlotsDir, "b")
	if err := os.MkdirAll(targetSlotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(targetSlotDir, ".trainboard-stale.tmp")
	if err := os.WriteFile(stale, []byte("leftover from interrupted download"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.applier.Apply(context.Background(), f.release); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp file not swept: err=%v", err)
	}
}

func TestOtherSlot(t *testing.T) {
	if otherSlot("a") != "b" || otherSlot("b") != "a" {
		t.Error("otherSlot broken")
	}
}
