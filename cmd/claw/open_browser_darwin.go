//go:build web && darwin

package main

import "os/exec"

// openBrowser opens the given URL in the default browser on macOS
func openBrowser(url string) error {
	return exec.Command("open", url).Start()
}
