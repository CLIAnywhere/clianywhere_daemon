//go:build windows

package main

import "os"

// initAlternateScreen Windows: CMD does not support alternate screen, clear screen directly
func initAlternateScreen() {
	os.Stdout.Write([]byte{'\033', '[', '2', 'J', '\033', '[', 'H'})
	os.Stdout.Write([]byte{'\033', '[', '?', '2', '5', 'l'})
}

// restoreAlternateScreen Windows: restore cursor (no alternate screen to restore)
func restoreAlternateScreen() {
	os.Stdout.Write([]byte{'\033', '[', '?', '2', '5', 'h'})
	os.Stdout.Write([]byte{'\n'})
}
