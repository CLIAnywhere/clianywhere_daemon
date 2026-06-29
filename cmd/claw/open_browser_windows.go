//go:build web && windows

package main

import "os/exec"

// openBrowser opens the given URL in the default browser on Windows
func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
