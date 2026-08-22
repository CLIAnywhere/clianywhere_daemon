//go:build linux

package main

import "os/exec"

// showAlert tries the common Linux dialog tools in order (GNOME / KDE / bare
// X11). If none is available (headless system), falls back to the daemon log.
func showAlert(title, text string) {
	candidates := [][]string{
		{"zenity", "--warning", "--title", title, "--text", text},
		{"kdialog", "--msgbox", text, "--title", title},
		{"xmessage", "-center", text},
	}
	for _, args := range candidates {
		if _, err := exec.LookPath(args[0]); err == nil {
			exec.Command(args[0], args[1:]...).Start() //nolint:errcheck
			return
		}
	}
	writeEarlyLog("alert (no dialog tool available): " + title + ": " + text)
}
