//go:build darwin

package main

import "os/exec"

// removeQuarantineAttr drops the com.apple.quarantine extended attribute if it
// was inherited (e.g. the daemon binary itself was downloaded). The freshly
// written .command file normally does not have it, but invoking xattr when
// there is nothing to remove exits non-zero — we ignore that case.
func removeQuarantineAttr(path string) error {
	return exec.Command("xattr", "-d", "com.apple.quarantine", path).Run()
}
