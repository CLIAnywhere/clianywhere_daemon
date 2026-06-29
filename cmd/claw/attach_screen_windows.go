//go:build cli && windows

package main

import "os"

// useSystemCursor: Windows uses the terminal's native cursor instead of rendering a custom one.
// The native cursor is positioned at xterm-go's cursor position after each render,
// and the terminal handles blinking natively — no custom blink ticker needed.
const useSystemCursor = true

// initAlternateScreen Windows: CMD does not support alternate screen, clear screen + scrollback
func initAlternateScreen() {
	os.Stdout.Write([]byte{'\033', '[', '2', 'J', '\033', '[', '3', 'J', '\033', '[', 'H'})
	// NOTE: not hiding cursor — useSystemCursor = true, let system cursor show
}

// restoreAlternateScreen Windows: restore cursor (no alternate screen to restore)
func restoreAlternateScreen() {
	os.Stdout.Write([]byte{'\033', '[', '?', '2', '5', 'h'})
	os.Stdout.Write([]byte{'\n'})
}
