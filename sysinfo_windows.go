//go:build windows

package main

import (
	"os/exec"
	"strings"
	"syscall"
)

func getWindowsVersion() (string, error) {
	cmd := exec.Command("cmd", "/c", "ver")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return "Windows " + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Microsoft ")), nil
}
