//go:build darwin

package checkupdate

import "errors"

// platformAssets lists the release asset names for this platform, in
// priority order. Used to pick the right download when updating.
func platformAssets() []string {
	return []string{
		"CLIAnywhere-macos.dmg", // macOS universal installer (Intel + Apple Silicon)
	}
}

// ApplyUpdate downloads and installs the update. Not implemented yet for macOS.
func ApplyUpdate(res *Result) error {
	return errors.New("checkupdate: self-update not implemented for darwin yet")
}
