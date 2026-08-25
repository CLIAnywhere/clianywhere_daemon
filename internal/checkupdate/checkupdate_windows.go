//go:build windows

package checkupdate

import (
	"fmt"
	"os"
	"os/exec"
)

// platformAssets lists the release asset names for this platform, in
// priority order. Used to pick the right download when updating.
func platformAssets() []string {
	return []string{
		"clianywhere-setup.exe", // Windows GUI installer (NSIS)
	}
}

// ApplyUpdate downloads the platform installer into the process directory and
// runs it silently (NSIS /S). The installer replaces the installed files, so
// this process must exit right after launching it to release the file lock —
// ApplyUpdate never returns on success.
func ApplyUpdate(res *Result) error {
	asset := PlatformAsset(res.Release)
	if asset == nil {
		return fmt.Errorf("checkupdate: no windows asset in release %s", res.Release.Name)
	}

	path, err := Download(asset)
	if err != nil {
		return err
	}

	// /S = NSIS silent install; detached — we exit before it touches files.
	cmd := exec.Command(path, "/S")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("checkupdate: start installer: %w", err)
	}

	// Let the installer replace our own executable.
	os.Exit(0)
	return nil // unreachable, keeps the signature honest
}
