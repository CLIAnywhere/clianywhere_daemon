//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	messageBoxWProc = user32.NewProc("MessageBoxW")
)

// showAlert shows a native Windows message box (blocking until dismissed —
// call from a goroutine). Used to reach the user in windowsgui builds that
// have no console, e.g. when the browser fails to open and the web terminal
// URL must be presented manually.
func showAlert(title, text string) {
	messageBoxWProc.Call(0,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(text))),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(title))),
		0x00000030|0x00010000, // MB_ICONWARNING | MB_SETFOREGROUND
	)
}
