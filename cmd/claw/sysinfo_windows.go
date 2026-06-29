//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func getWindowsVersion() (string, error) {
	// get shell path from environment variable at runtime
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		if p, err := exec.LookPath(shellCmd()); err == nil {
			shell = p
		} else {
			shell = shellCmd()
		}
	}
	cmd := exec.Command(shell, buildStr([]byte{47, 99}), buildStr([]byte{118, 101, 114}))
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
