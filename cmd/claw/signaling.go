package main

// ========== DATA STRUCTURES ==========

// TurnServerEntry TS address info, Addr is full WebSocket URL (e.g. wss://abc:port or ws://abc:port)
type TurnServerEntry struct {
	Addr string
	// Health load ratio from /health probe (lower = more idle, 1.0 = probe failed)
	Health float64
	// Local true if the server is in this machine's continent (ranked first)
	Local bool
}

// WSURL returns full WebSocket connection URL (directly uses addr reported by TS)
func (e TurnServerEntry) WSURL() string {
	return e.Addr
}
