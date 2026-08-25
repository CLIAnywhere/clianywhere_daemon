package checkupdate

import (
	"os"
	"testing"
)

// TestCheckUpdateAndDownload hits the real public test repo. It runs in the
// go-test temp process directory, so the downloaded asset never touches the
// source tree.
func TestCheckUpdateAndDownload(t *testing.T) {
	res, err := CheckUpdate("0.0.1") // force "update available" against any release
	if err != nil {
		t.Fatalf("CheckUpdate: %v", err)
	}
	if res.Latest == "" || res.Release == nil {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !res.Available {
		t.Fatalf("expected Available=true with current 0.0.1, latest %s", res.Latest)
	}

	asset := PlatformAsset(res.Release)
	if asset == nil {
		t.Fatalf("no platform asset in release %s", res.Release.Name)
	}

	path, err := Download(asset)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() != asset.Size {
		t.Fatalf("downloaded size %d != asset size %d", info.Size(), asset.Size)
	}
}

func TestParseAndCompareVersions(t *testing.T) {
	if got := ParseVersion("v1.0.1"); got != "1.0.1" {
		t.Fatalf("ParseVersion(v1.0.1) = %q", got)
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.1", "1.0.1", 0},
		{"1.0.2", "1.0.1", 1},
		{"1.0.1", "1.0.2", -1},
		{"1.1", "1.0.9", 1},
		{"2.0", "1.9.9", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Fatalf("CompareVersions(%s,%s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
