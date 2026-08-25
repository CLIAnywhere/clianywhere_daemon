// Package checkupdate queries the daemon's GitHub Releases for the latest
// version and compares it with the running daemon version. Later steps will
// add platform-specific download/install logic (see the _windows/_darwin/
// _linux files).
package checkupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// apiURL is the GitHub "latest release" endpoint of the daemon release
	// repo. The repo is public, so no authentication is needed.
	apiURL = "https://api.github.com/repos/hujun236/daemontest/releases/latest"

	// httpTimeout bounds the whole API request.
	httpTimeout = 15 * time.Second

	// downloadTimeout bounds one asset download. Installers are ~20-40 MB.
	downloadTimeout = 10 * time.Minute
)

// Public download URL pattern for reference:
//
//	https://github.com/hujun236/daemontest/releases/download/v<version>/<asset>
//
// Downloads go through the GitHub API asset endpoint (Asset.URL, filled by
// the releases API) with Accept: application/octet-stream, which works for
// public repos without authentication and (with a token) for private ones.

// Release holds the fields of the GitHub release response we care about.
type Release struct {
	Name    string  `json:"name"`     // release title, e.g. "v1.0.1"
	TagName string  `json:"tag_name"` // e.g. "v1.0.1"
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	URL                string `json:"url"` // API endpoint; GET with octet-stream Accept to download
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// Result is the outcome of an update check.
type Result struct {
	Current   string  // running daemon version (e.g. "1.0.1")
	Latest    string  // latest release version, leading "v" stripped
	Available bool    // true when a newer release exists
	Release   *Release
}

// CheckUpdate fetches the latest release and compares it with current.
// The version is taken from the release name (e.g. "v1.0.1"), with the
// leading "v" stripped before a numeric component-wise comparison.
func CheckUpdate(current string) (*Result, error) {
	rel, err := fetchLatest()
	if err != nil {
		return nil, err
	}

	latest := ParseVersion(rel.Name)
	if latest == "" {
		return nil, fmt.Errorf("checkupdate: cannot parse version from release name %q", rel.Name)
	}

	return &Result{
		Current:   current,
		Latest:    latest,
		Available: CompareVersions(latest, current) > 0,
		Release:   rel,
	}, nil
}

// fetchLatest calls the GitHub API for the latest release.
func fetchLatest() (*Release, error) {
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("checkupdate: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "clianywhere-daemon")

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checkupdate: request latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("checkupdate: github api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("checkupdate: decode release json: %w", err)
	}
	return &rel, nil
}

// PlatformAsset returns the release asset matching the current platform
// (see the per-platform files), or nil if none matches.
func PlatformAsset(rel *Release) *Asset {
	names := platformAssets()
	for _, want := range names {
		for i := range rel.Assets {
			if rel.Assets[i].Name == want {
				return &rel.Assets[i]
			}
		}
	}
	return nil
}

// Download fetches the asset (with token, via the API endpoint) and saves it
// into the directory of the running process. Returns the saved file path.
func Download(asset *Asset) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("checkupdate: locate process: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dest := filepath.Join(filepath.Dir(exe), asset.Name)

	req, err := http.NewRequest(http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", fmt.Errorf("checkupdate: build download request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "clianywhere-daemon")

	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("checkupdate: download %s: %w", asset.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checkupdate: download %s: status %d", asset.Name, resp.StatusCode)
	}

	// Write to a .part file first, rename on success, so a partial download
	// never masquerades as a usable installer/binary.
	tmp := dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("checkupdate: create %s: %w", tmp, err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("checkupdate: save %s: %w", asset.Name, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("checkupdate: save %s: %w", asset.Name, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("checkupdate: rename %s: %w", asset.Name, err)
	}
	return dest, nil
}

// ParseVersion extracts the version string from a release name/tag,
// stripping the leading "v" (e.g. "v1.0.1" -> "1.0.1").
func ParseVersion(name string) string {
	s := strings.TrimSpace(name)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return s
}

// CompareVersions compares two dotted numeric versions component-wise.
// Returns >0 if a is newer than b, 0 if equal, <0 if a is older.
func CompareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = atoiSafe(as[i])
		}
		if i < len(bs) {
			bv = atoiSafe(bs[i])
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}

// atoiSafe parses a non-negative integer, returning 0 on failure.
func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
