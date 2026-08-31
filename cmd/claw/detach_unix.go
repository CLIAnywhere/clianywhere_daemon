//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

var _daemonized = false

// forkToBackground forks child process to run daemon in background.
// Returns the child's reported port (>0) in parent process, or 0 if already daemonized child.
// Parent should use the port to connect to daemon via WS.
func forkToBackground() int {
	if os.Getenv("CLIANYWHERE_DAEMONIZED") == "1" {
		_daemonized = true
		return 0 // already the child process
	}

	// Create pipe: child writes port, parent reads
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create pipe: %v\n", err)
		return 0
	}

	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Env = append(os.Environ(), "CLIANYWHERE_DAEMONIZED=1")
	// Detach from the terminal session: closing the terminal sends SIGHUP to
	// the foreground process group; without setsid the daemon child stays in
	// it and dies together with the CLI menu process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{w} // fd 3 in child
	// redirect child stdout/stderr to log file
	if logF, logErr := openDaemonLogFile(); logErr == nil {
		cmd.Stdout = logF
		cmd.Stderr = logF
	}
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		fmt.Fprintf(os.Stderr, "Failed to fork: %v\n", err)
		return 0
	}
	w.Close()

	// Read port from child (blocks until child writes)
	buf := make([]byte, 32)
	n, _ := r.Read(buf)
	r.Close()
	portStr := strings.TrimSpace(string(buf[:n]))
	port, _ := strconv.Atoi(portStr)

	return port
}

// notifyParentPort writes the port to fd 3 (pipe to parent process)
func notifyParentPort(port int) {
	if !_daemonized {
		return
	}
	f := os.NewFile(3, "pipe")
	if f != nil {
		f.WriteString(strconv.Itoa(port))
		f.Close()
	}
}

// isDaemonized returns true if this is a forked child process
func isDaemonized() bool {
	return _daemonized
}

// openDaemonLogFile opens ~/.clianywhere/daemon.log for child process output
func openDaemonLogFile() (*os.File, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	os.MkdirAll(home+"/"+accessKeyDir, 0700)
	return os.OpenFile(home+"/"+accessKeyDir+"/"+logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}
