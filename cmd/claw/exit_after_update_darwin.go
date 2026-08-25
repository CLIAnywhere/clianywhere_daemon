//go:build darwin

package main

import (
	"os"
	"time"
)

// updateNoticeText is the final apply_update notice for this platform.
func updateNoticeText() string {
	return "update package opened; drag CLIAnywhere.app to Applications and restart the server"
}

// exitAfterUpdate runs after the update flow notified the web UI. On macOS
// the dmg is open in Finder for a manual drag install; the old daemon quits
// (brief delay so the WS message flushes), and the user restarts the server
// by launching the freshly dragged app.
func exitAfterUpdate() {
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
