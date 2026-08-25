//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Auto-run on Linux: systemd user service (~/.config/systemd/user/). One
// mechanism covers both desktop sessions and headless boxes; combine with
// `loginctl enable-linger` for run-without-login (user's choice, we don't
// force it). Environments without systemd (WSL default, minimal distros) are
// reported as unsupported so the UI can disable the toggle.

const (
	autoRunUnitName = "clianywhere.service"
)

func systemdUserUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", autoRunUnitName), nil
}

// autoRunSupported checks that a systemd user session manager is reachable.
func autoRunSupported() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	// /run/systemd/system exists iff systemd is PID 1. The user manager is
	// reachable when the user has a XDG runtime dir (logged-in session).
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	return os.Getenv("XDG_RUNTIME_DIR") != ""
}

// hasAutoRun reports whether the unit file exists AND ExecStart points at the
// current executable.
func hasAutoRun() bool {
	exePath, err := resolvedExePath()
	if err != nil {
		return false
	}
	unitPath, err := systemdUserUnitPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(unitPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		exec := strings.TrimSpace(strings.TrimPrefix(line, "ExecStart="))
		exec = strings.Trim(exec, `"`)
		if i := strings.IndexByte(exec, ' '); i >= 0 {
			exec = exec[:i]
		}
		if samePath(exec, exePath) {
			return true
		}
	}
	return false
}

// enableAutoRun installs the user unit and enables it (no --now: the daemon is
// already running under the user's manual instance; systemd starts its own at
// the next login, where the single-instance lock makes the latecomer exit).
func enableAutoRun() error {
	exePath, err := resolvedExePath()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	if !autoRunSupported() {
		return fmt.Errorf("systemd user session not available (unsupported environment)")
	}

	unitPath, err := systemdUserUnitPath()
	if err != nil {
		return fmt.Errorf("resolve unit path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}

	unit := fmt.Sprintf(`[Unit]
Description=CLIAnywhere local web terminal

[Service]
ExecStart=%s --autostart

[Install]
WantedBy=default.target
`, exePath)

	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	if err := runSystemctl("daemon-reload", "enable", autoRunUnitName); err != nil {
		return err
	}
	// Linger makes the user manager (and with it our service) start at BOOT
	// instead of first login — essential for a remote-access daemon. Best
	// effort: on distros where enable-linger needs admin auth it fails, and
	// the service then still starts at first login.
	setLinger(true)
	return nil
}

// disableAutoRun disables the unit and removes the unit file. Not stopping a
// running service: the currently running instance is the user's manual one.
func disableAutoRun() error {
	unitPath, err := systemdUserUnitPath()
	if err != nil {
		return fmt.Errorf("resolve unit path: %w", err)
	}
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return nil
	}
	if err := runSystemctl("disable", autoRunUnitName); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("remove unit: %w", err)
	}
	if err := runSystemctl("daemon-reload"); err != nil {
		return err
	}
	// Also undo linger so nothing of ours keeps the user manager alive.
	setLinger(false)
	return nil
}

// setLinger enables/disables loginctl linger for the current user. Best
// effort: some distros require admin authentication for this, in which case
// we log and fall back to login-triggered start.
func setLinger(enable bool) {
	if _, err := exec.LookPath("loginctl"); err != nil {
		return
	}
	verb := "disable-linger"
	if enable {
		verb = "enable-linger"
	}
	out, err := exec.Command("loginctl", verb, fmt.Sprint(os.Getuid())).CombinedOutput()
	if err != nil {
		writeEarlyLog(fmt.Sprintf("[AUTORUN] loginctl %s failed (autostart will trigger at login instead of boot): %v: %s",
			verb, err, strings.TrimSpace(string(out))))
	} else {
		writeEarlyLog(fmt.Sprintf("[AUTORUN] loginctl %s ok", verb))
	}
}

// runSystemctl executes `systemctl --user <args...>` and wraps failures with
// the combined output for display in the UI.
func runSystemctl(args ...string) error {
	full := append([]string{"--user"}, args...)
	out, err := exec.Command("systemctl", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
