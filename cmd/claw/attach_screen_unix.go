//go:build !windows

package main

import "os"

// initAlternateScreen Unix: enter alternate screen buffer, restore original terminal on exit
func initAlternateScreen() {
	os.Stdout.Write([]byte{'\033', '[', '?', '1', '0', '4', '9', 'h'})
	os.Stdout.Write([]byte{'\033', '[', '?', '2', '5', 'l'})
	os.Stdout.Write([]byte{'\033', '[', '2', 'J', '\033', '[', 'H'})
}

// restoreAlternateScreen Unix: exit alternate screen + restore cursor
func restoreAlternateScreen() {
	os.Stdout.Write([]byte{'\033', '[', '?', '2', '5', 'h', '\033', '[', '?', '1', '0', '4', '9', 'l'})
}
