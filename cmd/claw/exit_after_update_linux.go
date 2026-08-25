//go:build linux

package main

// exitAfterUpdate is a no-op on Linux: self-update is not implemented yet,
// so this is never reached.
func exitAfterUpdate() {}
