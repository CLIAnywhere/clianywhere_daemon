//go:build darwin

package checkupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

// findAppBundle walks up from the executable looking for the enclosing
// .app bundle directory (…/CLIAnywhere.app/Contents/MacOS/claw →
// …/CLIAnywhere.app). Returns "" for a bare binary.
func findAppBundle(exe string) string {
	dir := filepath.Dir(exe)
	for {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached root without an .app
		}
		dir = parent
	}
}

// mountDmg attaches a disk image read-only and returns the mount point.
func mountDmg(dmgPath string) (string, error) {
	out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-readonly", "-plist", dmgPath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("hdiutil: %s: %w", strings.TrimSpace(string(ee.Stderr)), err)
		}
		return "", fmt.Errorf("checkupdate: mount %s: %w", dmgPath, err)
	}

	// parse the plist output: <key>mount-point</key> followed by <string>
	for lines, i := strings.Split(string(out), "\n"), 0; i < len(lines)-1; i++ {
		if strings.TrimSpace(lines[i]) == "<key>mount-point</key>" {
			line := strings.TrimSpace(lines[i+1])
			if strings.HasPrefix(line, "<string>") && strings.HasSuffix(line, "</string>") {
				return strings.TrimSuffix(strings.TrimPrefix(line, "<string>"), "</string>"), nil
			}
		}
	}
	return "", fmt.Errorf("checkupdate: no mount-point in hdiutil output")
}

// firstAppBundle returns the first *.app entry directly inside dir.
func firstAppBundle(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// ApplyUpdate downloads the DMG and replaces the running .app bundle in
// place, then relaunches the new binary. Unlike Windows there is no file
// locking on macOS: the running process keeps its image even after the
// bundle is deleted, so replace-then-restart is safe.
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
	// replacing that bundle would update a throwaway copy.
	if isTransientLocation(exe) {
		return fmt.Errorf("checkupdate: app is running from the mounted disk image; drag CLIAnywhere.app to /Applications and run it from there to update")
	}

	bundle := findAppBundle(exe)
	if bundle == "" {
		return fmt.Errorf("checkupdate: not running from an .app bundle (bare binary installs cannot self-update)")
	}
	logger.Infof("[UPDATE] bundle: %s", bundle)

	// Download lands in the process dir (inside the old bundle); move it to
	// a temp location so the bundle swap below cannot destroy its backing
	// file while mounted.
	dmgPath, err := Download(asset)
	if err != nil {
		return err
	}
	tmpDmg := filepath.Join(os.TempDir(), asset.Name)
	if err := os.Rename(dmgPath, tmpDmg); err != nil {
		os.Remove(dmgPath)
		return fmt.Errorf("checkupdate: stage dmg: %w", err)
	}
	defer os.Remove(tmpDmg)

	mountPoint, err := mountDmg(tmpDmg)
	if err != nil {
		return err
	}
	defer func() {
		if err := exec.Command("hdiutil", "detach", mountPoint).Run(); err != nil {
			logger.Errorf("[UPDATE] detach %s failed: %v", mountPoint, err)
		}
	}()

	src := firstAppBundle(mountPoint)
	if src == "" {
		return fmt.Errorf("checkupdate: no .app found in %s", mountPoint)
	}

	// Replace the whole bundle. Deleting the running app is safe on macOS —
	// no file locks.
	//
	// IMPORTANT: the copy itself must be done BY FINDER, not by us (ditto/cp
	// as our child). Since macOS Ventura (tightened in Sequoia) the "App
	// Management" TCC protection SIGKILLs any process — including an app
	// updating itself — that writes into an app bundle under /Applications.
	// Finder is allowed to do it, so we send it an Apple event instead. The
	// first run shows a one-time "CLIAnywhere wants to control Finder"
	// prompt (NSAppleEventsUsageDescription covers it in Info.plist).
	logger.Infof("[UPDATE] replacing %s with %s (via Finder)", bundle, src)
	if err := os.RemoveAll(bundle); err != nil {
		return fmt.Errorf("checkupdate: remove old bundle: %w", err)
	}
	destDir := filepath.Dir(bundle)
	script := fmt.Sprintf(`tell application "Finder" to duplicate POSIX file %q to POSIX file %q`, src, destDir)
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		if strings.Contains(strings.ToLower(string(out)), "not authorized") ||
			strings.Contains(string(out), "-1743") {
			return fmt.Errorf("checkupdate: Finder automation denied: allow CLIAnywhere to control Finder in System Settings > Privacy & Security > Automation, or update manually: %s", string(out))
		}
		return fmt.Errorf("checkupdate: Finder copy: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Belt and braces: strip any quarantine flag so Gatekeeper never
	// prompts on the replaced bundle (the daemon download does not set it,
	// but ditto could copy one from the mounted image).
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", bundle).Run()

	// Relaunch the new binary detached (new session so terminal death does
	// not take it down), then exit this old process.
	logger.Infof("[UPDATE] relaunching %s --autostart", exe)
	cmd := exec.Command(exe, "--autostart")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logger.Errorf("[UPDATE] relaunch failed: %v", err)
		return fmt.Errorf("checkupdate: relaunch: %w", err)
	}
	os.Exit(0)
	return nil // unreachable, keeps the signature honest
}
