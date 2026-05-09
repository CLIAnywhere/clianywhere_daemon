//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// daemonize on Unix: daemonization via fork
// parent exits, child restarts from main() (detects CLIANYWHERE_DAEMONIZED env var)
func daemonize() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Printf("daemonize failed: %v\n", err)
		return
	}

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "CLIANYWHERE_DAEMONIZED=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		fmt.Printf("daemonize failed: %v\n", err)
		return
	}
	os.Exit(0)
}
