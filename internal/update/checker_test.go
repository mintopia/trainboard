package update

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/tz"
)

// windowNowConfig returns a Config whose unattended-update window covers
// "now" (via a wide powersaving window straddling the current moment), so
// tests calling tick() directly don't depend on wall-clock time happening
// to land inside the default 03:00-05:00 window.
func windowNowConfig(autoApply bool) config.Config {
	var cfg config.Config
	cfg.Update.AutoApply = autoApply
	now := time.Now().In(tz.Location())
	cfg.Powersaving.Enabled = true
	cfg.Powersaving.Start = now.Add(-time.Hour).Format("15:04")
	cfg.Powersaving.End = now.Add(time.Hour).Format("15:04")
	return cfg
}

// checkerFixture: a releases feed server + seeded state + checker.
func newCheckerFixture(t *testing.T, releasesBody string, cfg config.Config, running string) *Checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releasesBody))
	}))
	t.Cleanup(srv.Close)
	client := NewClient()
	client.ReleasesURL = srv.URL

	statePath := filepath.Join(t.TempDir(), "state.json")
	seed := DefaultState()
	seed.RolledBackFrom = "v0.1.9"
	if err := SaveState(statePath, seed); err != nil {
		t.Fatal(err)
	}
	applier := &Applier{StatePath: statePath, Running: running, HTTP: srv.Client(), Log: testLogger()}
	return NewChecker(client, applier, cfg, true, testLogger())
}

func TestAvailableMirrorsCheckNowWithoutDiskRead(t *testing.T) {
	c := newCheckerFixture(t, releasesJSON, config.Config{}, "v0.1.0")
	if c.Available() {
		t.Error("Available() = true before any check has run")
	}
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	if !c.Available() {
		t.Error("Available() = false after CheckNow found a newer release")
	}
}

func TestCheckNowFindsNewerRelease(t *testing.T) {
	c := newCheckerFixture(t, releasesJSON, config.Config{}, "v0.1.0")
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	st := c.Status()
	if st.Available != "v0.2.0" {
		t.Errorf("Available = %q, want v0.2.0", st.Available)
	}
	if st.NotesURL == "" || st.LastCheck.IsZero() || st.LastError != "" {
		t.Errorf("Status = %+v", st)
	}
	if st.RolledBackFrom != "v0.1.9" {
		t.Errorf("RolledBackFrom = %q (must be read live from state)", st.RolledBackFrom)
	}
}

func TestCheckNowAlreadyCurrent(t *testing.T) {
	c := newCheckerFixture(t, releasesJSON, config.Config{}, "v0.2.0")
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Status().Available; got != "" {
		t.Errorf("Available = %q, want empty (running is current)", got)
	}
}

func TestCheckNowDevBuildSeesUpdate(t *testing.T) {
	c := newCheckerFixture(t, releasesJSON, config.Config{}, "dev")
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Status().Available; got != "v0.2.0" {
		t.Errorf("Available = %q, want v0.2.0 (non-semver running never blocks)", got)
	}
}

func TestCheckNowPrereleaseChannel(t *testing.T) {
	cfg := config.Config{}
	cfg.Update.Channel = "prerelease"
	c := newCheckerFixture(t, releasesJSON, cfg, "v0.2.0")
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Status().Available; got != "v0.3.0-rc1" {
		t.Errorf("Available = %q, want v0.3.0-rc1", got)
	}
}

func TestCheckNowRecordsError(t *testing.T) {
	c := newCheckerFixture(t, `{"message":"boom"`, config.Config{}, "v0.1.0")
	if err := c.CheckNow(context.Background()); err == nil {
		t.Fatal("bad feed accepted")
	}
	if st := c.Status(); st.LastError == "" {
		t.Error("LastError not recorded")
	}
}

func TestApplyNowWithoutUpdateErrors(t *testing.T) {
	c := newCheckerFixture(t, `[]`, config.Config{}, "v0.1.0")
	if err := c.ApplyNow(context.Background()); err == nil {
		t.Error("ApplyNow with nothing available must error")
	}
}

func TestDisabledCheckerStatus(t *testing.T) {
	client := NewClient()
	applier := &Applier{StatePath: filepath.Join(t.TempDir(), "absent.json"), Running: "dev", Log: testLogger()}
	c := NewChecker(client, applier, config.Config{}, false, testLogger())
	st := c.Status()
	if st.Enabled {
		t.Error("disabled checker reports Enabled")
	}
	if st.Running != "dev" {
		t.Errorf("Running = %q", st.Running)
	}
	// Run must return immediately for a disabled checker.
	done := make(chan struct{})
	go func() { c.Run(context.Background(), func() {}); close(done) }()
	<-done
}

// TestTickSuppressesAutoApplyAfterRollback pins the fix for a nightly
// auto-apply that would otherwise keep re-installing a release the launcher
// just rolled back from — every 6h tick would find the same "available"
// release and reapply it, forever.
func TestTickSuppressesAutoApplyAfterRollback(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	st, err := LoadState(f.state)
	if err != nil {
		t.Fatal(err)
	}
	st.RolledBackFrom = f.release.Version // the ONLY available release was just rolled back
	if err := SaveState(f.state, st); err != nil {
		t.Fatal(err)
	}

	c := NewChecker(NewClient(), f.applier, windowNowConfig(true), true, testLogger())
	c.lastCheck = time.Now() // due check skipped; c.available below is what tick sees
	c.available = f.release

	restarted := false
	c.tick(context.Background(), func() { restarted = true })

	if restarted {
		t.Error("tick restarted: auto-apply must not re-apply a just-rolled-back version")
	}
	if _, err := os.Stat(filepath.Join(f.applier.SlotsDir, "b", "trainboard")); err == nil {
		t.Error("tick installed a release into the target slot despite the rollback marker matching it")
	}
}

// TestTickAppliesWhenRolledBackVersionDiffers is the companion case: a
// rollback marker for some OTHER (older) version must not block auto-apply
// of a genuinely newer release.
func TestTickAppliesWhenRolledBackVersionDiffers(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	st, err := LoadState(f.state)
	if err != nil {
		t.Fatal(err)
	}
	st.RolledBackFrom = "v9.9.9" // unrelated to the v0.2.0 release under test
	if err := SaveState(f.state, st); err != nil {
		t.Fatal(err)
	}

	c := NewChecker(NewClient(), f.applier, windowNowConfig(true), true, testLogger())
	c.lastCheck = time.Now()
	c.available = f.release

	restarted := false
	c.tick(context.Background(), func() { restarted = true })

	if !restarted {
		t.Error("tick did not apply an available release unrelated to the rollback marker")
	}
	if _, err := os.Stat(filepath.Join(f.applier.SlotsDir, "b", "trainboard")); err != nil {
		t.Errorf("expected the release installed into slot b: %v", err)
	}
}

// TestApplyNowSerializesConcurrentCalls pins the 211a173 fix: ApplyNow's
// applyMu must serialize the whole call end-to-end, so tick's unattended
// auto-apply and a user-triggered web handler can never run
// applier.Apply concurrently against the same target slot. The download
// handler sleeps briefly so two concurrent callers would overlap if
// applyMu weren't there; an atomic in-flight counter (checked under
// -race) proves they never do.
func TestApplyNowSerializesConcurrentCalls(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())

	var inFlight, maxInFlight int32
	// Wrap the fixture's server: intercept the asset download to sleep and
	// count concurrency, pass everything else through untouched.
	assetURL, _ := f.release.AssetURL("trainboard_v0.2.0_linux_arm64.gz")
	mux := http.NewServeMux()
	mux.HandleFunc("/slow-bin.gz", func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if n <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&maxInFlight, old, n) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		resp, err := f.applier.HTTP.Get(assetURL)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
	slowSrv := httptest.NewServer(mux)
	t.Cleanup(slowSrv.Close)
	for i, a := range f.release.Assets {
		if a.Name == "trainboard_v0.2.0_linux_arm64.gz" {
			f.release.Assets[i].URL = slowSrv.URL + "/slow-bin.gz"
		}
	}

	// A second concurrent ApplyNow call must be able to re-discover the
	// release (the winner clears c.available on success) without hitting
	// the real GitHub API: point the checker's feed client at a server
	// that serves this same release.
	relJSON, err := json.Marshal([]ghRelease{{
		TagName: f.release.Version,
		Assets: []ghAsset{
			{Name: "manifest.json", URL: mustAssetURL(t, f.release, "manifest.json")},
			{Name: "manifest.json.minisig", URL: mustAssetURL(t, f.release, "manifest.json.minisig")},
			{Name: "trainboard_v0.2.0_linux_arm64.gz", URL: mustAssetURL(t, f.release, "trainboard_v0.2.0_linux_arm64.gz")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	feedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(relJSON)
	}))
	t.Cleanup(feedSrv.Close)
	client := NewClient()
	client.ReleasesURL = feedSrv.URL

	c := NewChecker(client, f.applier, config.Config{}, true, testLogger())
	c.available = f.release

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = c.ApplyNow(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("ApplyNow[%d]: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&maxInFlight); got > 1 {
		t.Errorf("max concurrent downloads = %d, want 1 (ApplyNow must serialize)", got)
	}
}

// mustAssetURL is a small json-friendly wrapper around Release.AssetURL for
// building the fake releases-feed body above.
func mustAssetURL(t *testing.T, rel *Release, name string) string {
	t.Helper()
	url, ok := rel.AssetURL(name)
	if !ok {
		t.Fatalf("release has no asset %q", name)
	}
	return url
}
