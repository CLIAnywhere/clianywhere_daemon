//go:build windows

package main

import "syscall"

// windowsThreadID returns the current OS thread ID via GetCurrentThreadId
// (kernel32). Used only for diagnostic logging in the shortcut worker.
func windowsThreadID() uint32 {
	mod := syscall.NewLazyDLL("kernel32.dll")
	proc := mod.NewProc("GetCurrentThreadId")
	r, _, _ := proc.Call()
	return uint32(r)
}
