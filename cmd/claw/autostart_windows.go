//go:build windows

package main

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Auto-run on Windows: per-user HKCU Run key. Triggers at user login, needs
// no admin rights, and the entry is visible/manageable in Task Manager →
// Startup apps.

const (
	autoRunRegPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	autoRunValueName = "CLIAnywhere"
)

// autoRunSupported — HKCU Run works on every Windows install.
func autoRunSupported() bool { return true }

// hasAutoRun reports whether the Run entry exists AND points at the current
// executable. An entry pointing at another copy of the binary counts as OFF.
func hasAutoRun() bool {
	exePath, err := resolvedExePath()
	if err != nil {
		return false
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, autoRunRegPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	val, _, err := k.GetStringValue(autoRunValueName)
	if err != nil {
		return false
	}
	// Stored as `"C:\...\claw.exe" --autostart`; compare the quoted path part.
	val = strings.TrimSpace(val)
	val = strings.Trim(val, `"`)
	if i := strings.IndexByte(val, '"'); i >= 0 { // closing quote of the path
		val = val[:i]
	}
	return samePath(val, exePath)
}

// enableAutoRun writes the Run entry launching this exe with --autostart
// (silent start: no browser window at every login).
func enableAutoRun() error {
	exePath, err := resolvedExePath()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, autoRunRegPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()

	cmd := `"` + exePath + `" --autostart`
	if err := k.SetStringValue(autoRunValueName, cmd); err != nil {
		return fmt.Errorf("set Run value: %w", err)
	}
	return nil
}

// disableAutoRun deletes the Run entry.
func disableAutoRun() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoRunRegPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()

	if err := k.DeleteValue(autoRunValueName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("delete Run value: %w", err)
	}
	return nil
}
