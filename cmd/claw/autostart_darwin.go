//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Auto-run on macOS: per-user LaunchAgent. Requires macOS 10.11+ style
// launchctl; works on macOS 12 (our minimum) and everything newer. Pure Go:
// just a plist file + the launchctl CLI, no OC/Swift bridge needed.

const (
	autoRunLabel  = "cn.justtom.clianywhere"
	autoRunPlist  = "cn.justtom.clianywhere.plist"
	autoRunTmpDir = "/private/var/folders" // App Translocation root
)

func launchAgentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", autoRunPlist), nil
}

// autoRunSupported — LaunchAgents exist on every macOS install.
func autoRunSupported() bool { return true }

// hasAutoRun reports whether the LaunchAgent plist exists AND its program
// points at the current executable/bundle.
func hasAutoRun() bool {
	exePath, err := resolvedExePath()
	if err != nil {
		return false
	}
	plistPath, err := launchAgentPlistPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return false
	}
	// The plist we generate contains <string>/abs/path/to/binary</string>
	// followed by the --autostart argument. Match the full resolved path.
	content := string(data)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "<string>") || !strings.HasSuffix(line, "</string>") {
			continue
		}
		val := strings.TrimSuffix(strings.TrimPrefix(line, "<string>"), "</string>")
		if samePath(val, exePath) {
			return true
		}
	}
	return false
}

// enableAutoRun writes the LaunchAgent and loads it via launchctl.
func enableAutoRun() error {
	exePath, err := resolvedExePath()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}

	// App Translocation guard: if the app is running from a translocated
	// read-only path (launched straight from the DMG), the path is random per
	// boot and unusable for an autostart entry.
	if strings.HasPrefix(exePath, autoRunTmpDir) {
		return fmt.Errorf("app is running from a temporary location; move CLIAnywhere.app to /Applications and try again")
	}

	plistPath, err := launchAgentPlistPath()
	if err != nil {
		return fmt.Errorf("resolve plist path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	// RunAtLoad without KeepAlive: starts at login, but a deliberate stop
	// (Stop Server) must not be undone by launchd respawning us.
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--autostart</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`, autoRunLabel, exePath)

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	return loadLaunchAgent(plistPath)
}

// disableAutoRun unloads the agent and removes the plist.
func disableAutoRun() error {
	plistPath, err := launchAgentPlistPath()
	if err != nil {
		return fmt.Errorf("resolve plist path: %w", err)
	}
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return nil
	}
	unloadLaunchAgent()
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func loadLaunchAgent(plistPath string) error {
	uid := fmt.Sprintf("%d", os.Getuid())
	// Modern API first, legacy fallback for older setups.
	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, plistPath).CombinedOutput(); err == nil {
		return nil
	} else if out2, err2 := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err2 != nil {
		return fmt.Errorf("launchctl bootstrap/load failed: %v / %v: %s %s",
			err, err2, string(out), string(out2))
	}
	return nil
}

func unloadLaunchAgent() {
	uid := fmt.Sprintf("%d", os.Getuid())
	// Best-effort: both may fail if the agent was never loaded — that's fine.
	_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/"+autoRunLabel).Run()
	plistPath, err := launchAgentPlistPath()
	if err == nil {
		_ = exec.Command("launchctl", "unload", plistPath).Run()
	}
}
