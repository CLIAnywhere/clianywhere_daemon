//go:build linux && !cli

package checkupdate

// platformAssets for the web/desktop build (and default builds): update
// from the desktop zip. The cli build overrides this in
// checkupdate_linux_cli.go.
func platformAssets() []string {
	return []string{
		"claw-linux-desktop-amd64.zip", // Linux web build
	}
}
