//go:build windows

package main

// forkToBackground is a no-op on Windows (-H windowsgui already background).
// Returns 0 (no fork happened).
func forkToBackground() int {
	return 0
}

// notifyParentPort is a no-op on Windows.
func notifyParentPort(port int) {
	// no-op
}

// isDaemonized always returns false on Windows (no fork)
func isDaemonized() bool {
	return false
}
