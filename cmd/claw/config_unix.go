//go:build !windows

package main

import (
	"os/exec"
)

func ResolveShell(shell string) string {
	switch shell {
	case "", "cmd":
		return defaultShell()
	case "bash":
		return "/bin/bash"
	case "zsh":
		return "/bin/zsh"
	case "sh":
		return "/bin/sh"
	default:
		return shell
	}
}

func defaultShell() string {
	for _, s := range []string{"/bin/bash", "/bin/zsh", "/bin/sh"} {
		if isExecutable(s) {
			return s
		}
	}
	return "/bin/sh"
}

func isExecutable(path string) bool {
	_, err := exec.LookPath(path)
	return err == nil
}
