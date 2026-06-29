//go:build cli && !windows

package main

import "os"

// readStdin read from stdin (Unix version)
// on Unix with raw mode (ISIG=0), Ctrl+Z reads as byte 0x1A normally, no special handling needed
func readStdin(buf []byte) (int, error) {
	return os.Stdin.Read(buf)
}
