package main

import (
	"os"
	"path/filepath"
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
