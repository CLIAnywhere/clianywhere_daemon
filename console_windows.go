//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procAllocConsole       = kernel32.NewProc("AllocConsole")
	procFreeConsole        = kernel32.NewProc("FreeConsole")
	procSetStdHandle       = kernel32.NewProc("SetStdHandle")
	procGetStdHandle       = kernel32.NewProc("GetStdHandle")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
)

const (
	stdInputHandle  uintptr = 0xFFFFFFF6 // -10
	stdOutputHandle uintptr = 0xFFFFFFF5 // -11
	stdErrorHandle  uintptr = 0xFFFFFFF4 // -12
)

// consoleAllocated tracks whether created by our AllocConsole, prevents releasing parent console
var consoleAllocated bool

// ensureConsole if no console, create one with AllocConsole and redirect stdio
func ensureConsole() {
	if consoleAllocated {
		return
	}
	r1, _, _ := procGetStdHandle.Call(stdOutputHandle)
	if r1 != 0 && uintptr(syscall.InvalidHandle) != r1 {
		// existing console (running from CMD) — still enable VT100 + UTF-8
		enableVTermOnConsole()
		setConsoleUTF8()
		return
	}

	procAllocConsole.Call()
	redirectStdio()
	enableVTermOnConsole()
	setConsoleUTF8()
	consoleAllocated = true
}

// releaseConsole close the console window we created
func releaseConsole() {
	if consoleAllocated {
		procFreeConsole.Call()
		consoleAllocated = false
	}
}

func redirectStdio() {
	in, err := syscall.Open("CONIN$", syscall.O_RDWR, 0)
	if err == nil {
		procSetStdHandle.Call(stdInputHandle, uintptr(in))
		os.Stdin = os.NewFile(uintptr(in), "stdin")
	}

	out, err := syscall.Open("CONOUT$", syscall.O_RDWR, 0)
	if err == nil {
		procSetStdHandle.Call(stdOutputHandle, uintptr(out))
		procSetStdHandle.Call(stdErrorHandle, uintptr(out))
		os.Stdout = os.NewFile(uintptr(out), "stdout")
		os.Stderr = os.NewFile(uintptr(out), "stderr")
	}
}

// enableVTermOnConsole enable VT100 escape sequences on newly created AllocConsole console
func enableVTermOnConsole() {
	for _, handle := range []uintptr{stdOutputHandle, stdErrorHandle} {
		h, _, _ := procGetStdHandle.Call(handle)
		if h == 0 {
			continue
		}
		var mode uint32
		r1, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
		if r1 != 0 {
			procSetConsoleMode.Call(h, uintptr(mode|0x0004)) // ENABLE_VIRTUAL_TERMINAL_PROCESSING
		}
	}
}

// setConsoleUTF8 sets console input/output code page to UTF-8 (65001)
// so that Unicode characters (e.g. QR code block chars ▀▄█) display correctly
func setConsoleUTF8() {
	procSetConsoleOutputCP.Call(65001)
	procSetConsoleCP.Call(65001)
}

// disableQuickEditMode disables CMD Quick Edit mode so that clicking the
// console window does not pause the process. Must be called after console is ready.
func disableQuickEditMode() {
	h, _, _ := procGetStdHandle.Call(stdInputHandle)
	if h == 0 {
		return
	}
	var mode uint32
	r1, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	if r1 != 0 {
		mode &^= 0x0040              // clear ENABLE_QUICK_EDIT_MODE
		mode |= 0x0080               // set ENABLE_EXTENDED_FLAGS (required for the above to take effect)
		procSetConsoleMode.Call(h, uintptr(mode))
	}
}
