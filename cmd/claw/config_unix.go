//go:build !windows

package main

import (
	"os/exec"
)

func defaultShellName() string {
	shell := defaultShell()
	switch shell {
	case "/bin/bash":
		return "bash"
	case "/bin/zsh":
		return "zsh"
	case "/bin/sh":
		return "sh"
	default:
		return "sh"
	}
}

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

// DetectShells returns available shells on Unix, default first
func DetectShells() []ShellInfo {
	var shells []ShellInfo

	// detect in priority order, default shell first
	candidates := []struct {
		name string
		path string
	}{
		{"bash", "/bin/bash"},
		{"zsh", "/bin/zsh"},
		{"fish", "/usr/bin/fish"},
		{"sh", "/bin/sh"},
		{"pwsh", ""}, // path resolved by LookPath
	}

	def := defaultShell()
	for _, c := range candidates {
		p := c.path
		if p == "" {
			if found, err := exec.LookPath(c.name); err == nil {
				p = found
			} else {
				continue
			}
		} else if !isExecutable(p) {
			continue
		}
		entry := ShellInfo{Name: c.name, Path: p}
		if p == def {
			// default goes first
			shells = append([]ShellInfo{entry}, shells...)
		} else {
			shells = append(shells, entry)
		}
	}

	return shells
}
