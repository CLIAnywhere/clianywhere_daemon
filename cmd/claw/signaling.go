package main

// ========== DATA STRUCTURES ==========

// TurnServerEntry TS address info, Addr is full WebSocket URL (e.g. wss://abc:port or ws://abc:port)
type TurnServerEntry struct {
	Addr string
}

// WSURL returns full WebSocket connection URL (directly uses addr reported by TS)
func (e TurnServerEntry) WSURL() string {
	return e.Addr
}
