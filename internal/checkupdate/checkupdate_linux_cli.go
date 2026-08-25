//go:build linux && cli

package checkupdate

// platformAssets for the CLI build: update from the cli zip. ApplyUpdate
// and helpers live in checkupdate_linux_common.go.
func platformAssets() []string {
	return []string{
		"claw-linux-cli-amd64.zip", // Linux CLI build
	}
}
