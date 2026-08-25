//go:build darwin

package main

import (
	"os"
	"time"
)

// exitAfterUpdate runs after the update flow notified the web UI. On macOS
// the dmg is open in Finder for a manual drag install; the old daemon quits
// (brief delay so the WS message flushes), and the user restarts the server
// by launching the freshly dragged app.
func exitAfterUpdate() {
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
