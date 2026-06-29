//go:build cli && !windows

package main

import "fmt"

// qrBindModeUI Unix stub: falls back to terminal-based binding
func qrBindModeUI(cfg *Config) (string, error) {
	return "", fmt.Errorf("GUI binding not supported on this platform, use terminal mode")
}
