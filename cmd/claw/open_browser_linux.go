//go:build web && linux

package main

import "os/exec"

// openBrowser opens the given URL in the default browser on Linux (via xdg-open)
func openBrowser(url string) error {
	return exec.Command("xdg-open", url).Start()
}
