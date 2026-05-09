//go:build windows

package main

import (
	"os"
	"os/exec"
)

func ResolveShell(shell string) string {
	switch shell {
	case "", "cmd":
		return defaultShell()
	case "powershell", "pwsh":
		return findPowerShell()
	default:
		return shell
	}
}

func defaultShell() string {
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		return comspec
	}
	return "cmd.exe"
}

func findPowerShell() string {
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p
	}
	if p, err := exec.LookPath("powershell"); err == nil {
		return p
	}
	return "powershell.exe"
}
