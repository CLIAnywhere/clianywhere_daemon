//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// userDesktopPath returns ~/Desktop on macOS. The user's home directory is
// localized only at Finder display level; the filesystem path is always "Desktop".
func userDesktopPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	desktop := filepath.Join(home, "Desktop")
	if fi, err := os.Stat(desktop); err == nil && fi.IsDir() {
		return desktop, nil
	}
	return "", fmt.Errorf("desktop directory not found")
}

// appBundlePath reports the enclosing .app bundle root when exePath lives inside
// one (e.g. CLIAnywhere.app/Contents/MacOS/claw), otherwise returns "".
func appBundlePath(exePath string) string {
	const marker = ".app" + string(filepath.Separator) + "Contents" + string(filepath.Separator) + "MacOS"
	if i := strings.LastIndex(exePath, marker); i >= 0 {
		return exePath[:i+len(".app")]
	}
	return ""
}

// createDesktopShortcut puts a launcher on the user's desktop.
//
// When running from a packaged CLIAnywhere.app (the current distribution) it
// symlinks the bundle onto the desktop so double-click launches the real app
// via LaunchServices and Finder shows the app icon. A bare binary build
// (go build / go run) still gets the legacy .command script that opens Terminal
// and execs the binary, keeping logs visible with Ctrl+C to stop.
func createDesktopShortcut() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	// Resolve symlinks so the path points at the real binary (homebrew, etc.).
	if real, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = real
	}

	desktop, err := userDesktopPath()
	if err != nil {
		return fmt.Errorf("resolve desktop: %w", err)
	}

	// Packaged .app: put a symlink to the bundle on the desktop.
	if appPath := appBundlePath(exePath); appPath != "" {
		// Remove the legacy .command entry so we don't leave a second launcher
		// pointing at the bare binary. Best-effort.
		_ = os.Remove(filepath.Join(desktop, "CLIAnywhere.command"))

		linkPath := filepath.Join(desktop, "CLIAnywhere.app")
		if fi, err := os.Lstat(linkPath); err == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				// A real file/dir already occupies the name; refuse to clobber it.
				return fmt.Errorf("%s exists and is not a symlink", linkPath)
			}
			// Rebuild the old symlink so it points at the current bundle.
			if err := os.Remove(linkPath); err != nil {
				return fmt.Errorf("replace old symlink: %w", err)
			}
		}
		if err := os.Symlink(appPath, linkPath); err != nil {
			return fmt.Errorf("symlink .app: %w", err)
		}
		return nil
	}

	// Bare binary: legacy .command script so Terminal stays attached (logs,
	// Ctrl+C to stop).
	workDir := filepath.Dir(exePath)
	commandPath := filepath.Join(desktop, "CLIAnywhere.command")
	script := fmt.Sprintf("#!/bin/bash\n# CliAnyWhere launcher\ncd %q\nexec %q\n",
		workDir, exePath)
	if err := os.WriteFile(commandPath, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write .command: %w", err)
	}
	// macOS requires the executable bit for .command files to be double-clickable.
	_ = os.Chmod(commandPath, 0o755)
	// Drop the com.apple.quarantine extended attribute if present (copied binary),
	// otherwise Terminal will refuse to run the script until the user clicks
	// through Gatekeeper. Best-effort: ignore errors.
	_ = removeQuarantineAttr(commandPath)
	return nil
}
