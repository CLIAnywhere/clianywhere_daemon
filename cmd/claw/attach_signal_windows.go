//go:build cli && windows

package main

// ignoreStopSignals no-op on Windows (no SIGTSTP)
func ignoreStopSignals() func() {
	return func() {}
}
