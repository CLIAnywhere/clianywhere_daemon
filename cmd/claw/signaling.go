package main

// ========== DATA STRUCTURES ==========

// TurnServerEntry TS address info, Addr is full WebSocket URL (e.g. wss://ts.clianywhere.com/ws)
// There is a single relay (the Durable Objects deployment), so the Health /
// Local sorting fields no longer exist.
type TurnServerEntry struct {
	Addr string
}

// WSURL returns full WebSocket connection URL (directly uses addr reported by TS)
func (e TurnServerEntry) WSURL() string {
	return e.Addr
}
