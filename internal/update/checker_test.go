package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/config"
)

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
