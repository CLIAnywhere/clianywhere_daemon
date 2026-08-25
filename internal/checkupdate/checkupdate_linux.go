//go:build linux

package checkupdate

import "errors"

// platformAssets lists the release asset names for this platform, in
// priority order. Used to pick the right download when updating.
func platformAssets() []string {
	return []string{
		"claw-linux-desktop-amd64.zip", // Linux web build
		"claw-linux-cli-amd64.zip",     // Linux CLI build
	}
}

// ApplyUpdate downloads and installs the update. Not implemented yet for Linux.
func ApplyUpdate(res *Result) error {
	return errors.New("checkupdate: self-update not implemented for linux yet")
}
