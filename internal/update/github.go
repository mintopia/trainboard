package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultReleasesURL is this repository's releases feed. Unauthenticated:
// one device checking every 6h is far inside GitHub's anonymous rate limit.
const defaultReleasesURL = "https://api.github.com/repos/mintopia/trainboard/releases"

// maxAPIResponse bounds how much of the releases feed we read (defensive;
// the feed for this repo is a few KB).
const maxAPIResponse = 1 << 20 // 1 MiB

// Asset is a downloadable release artifact.
type Asset struct {
	Name string
	URL  string
}

// Release is one GitHub release, reduced to what the updater needs.
type Release struct {
	Version    string // tag name, e.g. "v0.2.0"
	Prerelease bool
	NotesURL   string // release page, linked from the web UI
	Assets     []Asset
}

// AssetURL finds a release asset's download URL by exact name.
func (r *Release) AssetURL(name string) (string, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, true
		}
	}
	return "", false
}

// Client queries the GitHub releases API.
type Client struct {
	ReleasesURL string
	HTTP        *http.Client
}

// NewClient returns a production client (30s timeout: WiFi Pi, small JSON).
func NewClient() *Client {
	return &Client{ReleasesURL: defaultReleasesURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// ghRelease/ghAsset mirror the fields we read from the API document.
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Prerelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
	HTMLURL    string    `json:"html_url"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// LatestRelease returns the newest (API order: most recent first) non-draft
// release matching channel: "stable" skips prereleases, "prerelease"
// accepts anything. (nil, nil) when no release matches — a fresh repo, or a
// prerelease-only history on the stable channel.
func (c *Client) LatestRelease(ctx context.Context, channel string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ReleasesURL+"?per_page=10", nil)
	if err != nil {
		return nil, fmt.Errorf("update: building releases request: %w", err)
	}
	// GitHub's API requires a User-Agent.
	req.Header.Set("User-Agent", "trainboard-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetching releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: releases API returned %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return nil, fmt.Errorf("update: reading releases response: %w", err)
	}
	var rels []ghRelease
	if err := json.Unmarshal(raw, &rels); err != nil {
		return nil, fmt.Errorf("update: parsing releases response: %w", err)
	}
	for _, r := range rels {
		if r.Draft {
			continue
		}
		if channel != "prerelease" && r.Prerelease {
			continue
		}
		out := &Release{Version: r.TagName, Prerelease: r.Prerelease, NotesURL: r.HTMLURL}
		for _, a := range r.Assets {
			out.Assets = append(out.Assets, Asset(a))
		}
		return out, nil
	}
	return nil, nil
}
