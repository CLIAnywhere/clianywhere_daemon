//go:build darwin

package checkupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// platformAssets lists the release asset names for this platform, in
// priority order. Used to pick the right download when updating.
func platformAssets() []string {
	return []string{
		"CLIAnywhere-macos.dmg", // macOS universal installer (Intel + Apple Silicon)
	}
}

// isTransientLocation reports whether the app is running straight from a
// mounted disk image (/Volumes/...) or from an App Translocation path
// (/private/var/folders/... — macOS relocates quarantined apps launched
// directly from the DMG there). Updating from there is pointless: the
// bundle must be dragged to /Applications first. Mirrors the guard in
// cmd/claw/autostart_darwin.go.
func isTransientLocation(exePath string) bool {
	return strings.HasPrefix(exePath, "/Volumes/") ||
		strings.HasPrefix(exePath, "/private/var/folders/")
}

// ApplyUpdate downloads the DMG and opens it in Finder so the user can drag
// the app to /Applications manually.
//
// Deliberately no automated bundle replace: macOS "App Management" TCC
// protection (Ventura+, tightened in Sequoia) SIGKILLs processes that write
// into app bundles under /Applications — including an app updating itself —
// and working around it (Finder automation prompts, relaunch choreography)
// is not worth the complexity. The drag install takes the user one motion.
func ApplyUpdate(res *Result) error {
	asset := PlatformAsset(res.Release)
	if asset == nil {
		return fmt.Errorf("checkupdate: no macos asset in release %s", res.Release.Name)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("checkupdate: locate process: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// Guard: running straight from the mounted DMG (or translocated) —
	// opening another DMG from there is pointless.
	if isTransientLocation(exe) {
		return fmt.Errorf("checkupdate: app is running from the mounted disk image; drag CLIAnywhere.app to /Applications and run it from there to update")
	}

	dmgPath, err := Download(asset)
	if err != nil {
		return err
	}

	// Move the dmg to ~/Downloads (out of the running bundle) so the user
	// can also find it later; fall back to the downloaded path if the move
	// fails (e.g. cross-volume).
	openPath := dmgPath
	if home, err := os.UserHomeDir(); err == nil {
		if dest := filepath.Join(home, "Downloads", asset.Name); dest != dmgPath {
			if err := os.Rename(dmgPath, dest); err == nil {
				openPath = dest
			}
		}
	}

	// open mounts the dmg and shows its Finder window (drag-to-Applications
	// layout). No process restart — the new version runs after the user
	// replaces the bundle and launches it.
	logger.Infof("[UPDATE] opened %s — user drags the app to Applications", openPath)
	if out, err := exec.Command("open", openPath).CombinedOutput(); err != nil {
		return fmt.Errorf("checkupdate: open dmg: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
