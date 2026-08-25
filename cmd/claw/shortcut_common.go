package main

import (
	"os"
	"path/filepath"
	"strings"
)

// shortcutMarkerPath returns the persistent marker file path under ~/.clianywhere/.
// The marker exists iff the user has already been asked about a desktop shortcut.
// Same convention on every OS so the behavior is predictable.
func shortcutMarkerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clianywhere", ".shortcut_asked"), nil
}

// shouldAskShortcut reports true iff no marker file exists yet (i.e. this is the first ask).
func shouldAskShortcut() bool {
	p, err := shortcutMarkerPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return os.IsNotExist(err)
}

// markShortcutAsked writes the marker so we never ask again.
func markShortcutAsked() {
	p, err := shortcutMarkerPath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte("1"), 0o600)
}

// resolvedExePath returns the current executable path with symlinks resolved
// (homebrew installs, .app bundles, etc.). Shared by the shortcut and autostart
// code so every comparison uses the same canonical path.
func resolvedExePath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exePath); err == nil {
		return real, nil
	}
	return exePath, nil
}

// samePath compares two filesystem paths, case-insensitively on Windows.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return strings.EqualFold(a, b)
}
