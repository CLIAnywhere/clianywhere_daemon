package main

import (
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// findLocalWSPort scan 56300-56400, return first connectable port, -1 if not found
// shared by CLI and GUI modes
func findLocalWSPort() int {
	dialer := websocket.Dialer{HandshakeTimeout: 1 * time.Second}
	for port := 56300; port <= 56400; port++ {
		url := fmt.Sprintf("ws://127.0.0.1:%d", port)
		conn, _, err := dialer.Dial(url, nil)
		if err == nil {
			conn.Close()
			return port
		}
	}
	return -1
}
