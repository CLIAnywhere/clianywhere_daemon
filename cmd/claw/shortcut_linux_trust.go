//go:build linux

package main

import "os/exec"

// markDesktopTrusted sets the "metadata::trusted" attribute on the .desktop
// file via gio (GNOME/Nautilus). Without it, Nautilus shows an
// "Untrusted application launcher" prompt requiring a manual right-click →
// "Allow launching". Other DEs (KDE/XFCE) simply ignore the attribute.
//
// Best-effort: ignored if gio is missing or fails.
func markDesktopTrusted(path string) error {
	return exec.Command("gio", "set", path, "metadata::trusted", "true").Run()
}
