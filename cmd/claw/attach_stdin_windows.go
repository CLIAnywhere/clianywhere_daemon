//go:build cli && windows

package main

import (
	"io"
	"os"
)

// readStdin read from stdin, convert Windows console Ctrl+Z (EOF) to actual 0x1A byte
// Windows console always treats Ctrl+Z as EOF regardless of console mode, not as normal byte
// os.Stdin.Read() returns io.EOF + 0 bytes, detect and inject 0x1A here
func readStdin(buf []byte) (int, error) {
	n, err := os.Stdin.Read(buf)
	if err == io.EOF {
		// EOF from Ctrl+Z: inject actual byte value for forwarding to PTY
		// reaching passthrough means MakeRaw succeeded, stdin must be console not pipe
		buf[0] = 0x1A
		return 1, nil
	}
	return n, err
}
