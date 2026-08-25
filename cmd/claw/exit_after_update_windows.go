//go:build windows

package main

// updateNoticeText is never sent on Windows: ApplyUpdate exits the process
// right after launching the silent installer, before the handler could
// notify the web UI. Stub keeps the shared call site compiling.
func updateNoticeText() string { return "" }

// exitAfterUpdate is a no-op on Windows: ApplyUpdate itself never returns —
// it exits the process right after launching the silent installer.
func exitAfterUpdate() {}
