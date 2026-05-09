//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// daemonize on Windows: launch independent subprocess via DETACHED_PROCESS
// DETACHED_PROCESS detaches subprocess from parent console, CMD can immediately reclaim prompt
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
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("daemonize failed: %v\n", err)
		return
	}
	os.Exit(0)
}
