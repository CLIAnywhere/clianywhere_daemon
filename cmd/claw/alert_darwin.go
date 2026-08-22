//go:build darwin

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// showAlert shows a native macOS dialog via osascript (no cgo).
// Fire-and-forget: errors are ignored — there is no console to report to.
func showAlert(title, text string) {
	// escape for AppleScript double-quoted string
	esc := func(s string) string {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
		return r.Replace(s)
	}
	script := fmt.Sprintf(`display dialog "%s" with title "%s" buttons {"OK"} default button "OK" with icon caution`,
		esc(text), esc(title))
	exec.Command("osascript", "-e", script).Start() //nolint:errcheck
}
