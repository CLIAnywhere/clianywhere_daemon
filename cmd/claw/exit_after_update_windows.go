//go:build windows

package main

// exitAfterUpdate is a no-op on Windows: ApplyUpdate itself never returns —
// it exits the process right after launching the silent installer.
func exitAfterUpdate() {}
