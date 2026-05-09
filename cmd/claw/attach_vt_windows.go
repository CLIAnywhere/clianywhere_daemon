//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func init() {
	// Windows CMD does not parse ANSI escape sequences by default, need to manually enable Virtual Terminal Processing
	enableVTerm(os.Stdout.Fd())
	enableVTerm(os.Stderr.Fd())
}

func enableVTerm(fd uintptr) {
	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(fd), &mode); err != nil {
		return
	}
	windows.SetConsoleMode(windows.Handle(fd), mode|0x0004) // ENABLE_VIRTUAL_TERMINAL_PROCESSING
}
