//go:build windows

package checkupdate

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// platformAssets lists the release asset names for this platform, in
// priority order. Used to pick the right download when updating.
func platformAssets() []string {
	return []string{
		"clianywhere-setup.exe", // Windows GUI installer (NSIS)
	}
}

// ApplyUpdate downloads the platform installer into the process directory and
// runs it silently (NSIS /S). The installer replaces the installed files, so
// this process must exit right after launching it to release the file lock —
// ApplyUpdate never returns on success.
func ApplyUpdate(res *Result) error {
	asset := PlatformAsset(res.Release)
	if asset == nil {
		return fmt.Errorf("checkupdate: no windows asset in release %s", res.Release.Name)
	}

	path, err := Download(asset)
	if err != nil {
		return err
	}

	// /S = NSIS silent install. Launch fully detached so the installer
	// survives this process exiting (and being killed):
	//   DETACHED_PROCESS          — no console sharing (console death kills the group)
	//   CREATE_NEW_PROCESS_GROUP  — immune to parent's Ctrl+C / group signals
	//   CREATE_BREAKAWAY_FROM_JOB — escape job objects marked kill-on-close,
	//                               which would otherwise take the installer
	//                               down with the daemon
	logger.Infof("[UPDATE] launching installer (detached): %s /S", path)
	if err := startDetached(path, "/S"); err != nil {
		logger.Errorf("[UPDATE] failed to start installer %s: %v", path, err)
		return fmt.Errorf("checkupdate: start installer: %w", err)
	}

	// Let the installer replace our own executable.
	os.Exit(0)
	return nil // unreachable, keeps the signature honest
}

// startDetached launches a process detached from our console, process group
// and job object. Tries with CREATE_BREAKAWAY_FROM_JOB first (fails when the
// enclosing job does not permit breakaway), then falls back without it.
func startDetached(path string, args ...string) error {
	run := func(breakaway bool) error {
		cmd := exec.Command(path, args...)
		flags := uint32(windows.DETACHED_PROCESS) | windows.CREATE_NEW_PROCESS_GROUP
		if breakaway {
			flags |= windows.CREATE_BREAKAWAY_FROM_JOB
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		return cmd.Start()
	}
	if err := run(true); err != nil {
		return run(false)
	}
	return nil
}
