//go:build windows

package main

import "os"

// makeSigWinchListener Windows has no SIGWINCH signal, return nil channel
func makeSigWinchListener() (chan os.Signal, func()) {
	return nil, func() {}
}
