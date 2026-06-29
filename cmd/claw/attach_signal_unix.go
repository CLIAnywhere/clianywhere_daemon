//go:build cli && !windows

package main

import (
	"os/signal"
	"syscall"
)

// ignoreStopSignals ignore SIGTSTP (Ctrl+Z), prevent process from being stopped
func ignoreStopSignals() func() {
	signal.Ignore(syscall.SIGTSTP)
	return func() { signal.Reset(syscall.SIGTSTP) }
}
