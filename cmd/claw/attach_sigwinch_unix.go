//go:build cli && !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// makeSigWinchListener create SIGWINCH signal listener channel
// returns (channel, cleanup function)
func makeSigWinchListener() (chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}
