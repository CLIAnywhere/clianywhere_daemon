//go:build linux

package main

import (
	"os"
	"time"
)

// updateNoticeText is the final apply_update notice for this platform.
func updateNoticeText() string {
	return "update installed; restart the server"
}

// exitAfterUpdate runs after the update flow notified the web UI: the
// binary was replaced in place, the old daemon quits (brief delay so the
// WS message flushes) and the user restarts the server.
func exitAfterUpdate() {
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
