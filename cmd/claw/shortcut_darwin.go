//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
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

// createDesktopShortcut writes a .command file on the user's desktop that
// launches the daemon executable. Double-clicking opens Terminal, which stays
// attached to the daemon (intentional — user sees logs, Ctrl+C to stop).
func createDesktopShortcut() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	// Resolve symlinks so the script points at the real binary (homebrew, etc.).
	if real, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = real
	}
	workDir := filepath.Dir(exePath)

	desktop, err := userDesktopPath()
	if err != nil {
		return fmt.Errorf("resolve desktop: %w", err)
	}

	// 固定文件名为 CLIAnywhere.command，Finder 里直接显示为 CLIAnywhere。
	commandPath := filepath.Join(desktop, "CLIAnywhere.command")

	// Shell script content: cd to the binary's dir, exec it so Terminal's
	// process is the daemon itself — Ctrl+C in Terminal kills daemon cleanly.
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
