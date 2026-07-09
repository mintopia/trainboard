package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const releasesJSON = `[
  {"tag_name":"v0.3.0-rc1","prerelease":true,"draft":false,
   "html_url":"https://github.com/mintopia/trainboard/releases/tag/v0.3.0-rc1",
   "assets":[{"name":"manifest.json","browser_download_url":"https://dl/rc1/manifest.json"}]},
  {"tag_name":"v0.2.0","prerelease":false,"draft":false,
   "html_url":"https://github.com/mintopia/trainboard/releases/tag/v0.2.0",
   "assets":[
     {"name":"manifest.json","browser_download_url":"https://dl/v020/manifest.json"},
     {"name":"manifest.json.minisig","browser_download_url":"https://dl/v020/manifest.json.minisig"},
     {"name":"trainboard_v0.2.0_linux_arm64.gz","browser_download_url":"https://dl/v020/bin.gz"}]},
  {"tag_name":"v9.9.9","prerelease":false,"draft":true,
   "html_url":"https://github.com/mintopia/trainboard/releases/tag/v9.9.9","assets":[]}
]`

func newTestClient(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	c.ReleasesURL = srv.URL
	return c
}

func TestLatestReleaseStableSkipsPrereleaseAndDraft(t *testing.T) {
	c := newTestClient(t, http.StatusOK, releasesJSON)
	rel, err := c.LatestRelease(context.Background(), "stable")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel == nil || rel.Version != "v0.2.0" || rel.Prerelease {
		t.Fatalf("got %+v, want v0.2.0 stable", rel)
	}
	if url, ok := rel.AssetURL("manifest.json.minisig"); !ok || url != "https://dl/v020/manifest.json.minisig" {
		t.Errorf("AssetURL = %q,%v", url, ok)
	}
	if _, ok := rel.AssetURL("absent"); ok {
		t.Error("AssetURL found an absent asset")
	}
	if rel.NotesURL == "" {
		t.Error("NotesURL empty")
	}
}

func TestLatestReleasePrereleaseChannelTakesNewest(t *testing.T) {
	c := newTestClient(t, http.StatusOK, releasesJSON)
	rel, err := c.LatestRelease(context.Background(), "prerelease")
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || rel.Version != "v0.3.0-rc1" {
		t.Fatalf("got %+v, want v0.3.0-rc1", rel)
	}
}

func TestLatestReleaseNoneMatching(t *testing.T) {
	c := newTestClient(t, http.StatusOK, `[]`)
	rel, err := c.LatestRelease(context.Background(), "stable")
	if err != nil || rel != nil {
		t.Errorf("got %+v, %v; want nil, nil", rel, err)
	}
}

func TestLatestReleaseHTTPError(t *testing.T) {
	c := newTestClient(t, http.StatusForbidden, `{"message":"rate limited"}`)
	if _, err := c.LatestRelease(context.Background(), "stable"); err == nil {
		t.Error("HTTP 403 not surfaced as error")
	}
}
