//go:build !windows

package main

// consoleAllocated is always false on Unix
var consoleAllocated bool

// ensureConsole/releaseConsole no-op on Unix
func ensureConsole() {}
func releaseConsole() {}
func setConsoleUTF8() {}
func disableQuickEditMode() {}
