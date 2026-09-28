package main

import "strings"

// DefaultTSAddr is the fixed relay address.
//
// The relay runs on Cloudflare Durable Objects (worker project
// clianywhere-ts, custom domain ts.clianywhere.com): global anycast with
// automatic scaling, so there is no "node selection" to do. The earlier
// servers.json multi-node + /health probing + continent-sorting scheme was
// designed for a self-hosted VPS cluster and became obsolete after the DO
// migration — only this constant remains.
const DefaultTSAddr = "wss://ts.clianywhere.com/ws"

// forceTSAddr overrides the relay address (env CLIANYWHERE_TS), for local
// development and joint debugging.
var forceTSAddr string

// SetForceTSAddr sets the forced relay address (called once at startup).
func SetForceTSAddr(addr string) {
	forceTSAddr = strings.TrimSpace(addr)
}

// loadForceTSAddr returns the forced address, or "" when unset.
func loadForceTSAddr() string {
	return forceTSAddr
}

// ResolveTSAddr returns the relay address to connect to.
//
// Priority: ForceTSAddr (env CLIANYWHERE_TS) > DefaultTSAddr.
// The returned address looks like wss://ts.clianywhere.com/ws — the caller
// appends ?role=daemon&k=<accesskey> directly.
func ResolveTSAddr() string {
	if forced := loadForceTSAddr(); forced != "" {
		return forced
	}
	return DefaultTSAddr
}

// Local debugging: CLIANYWHERE_TS=ws://127.0.0.1:8787/ws ./daemon run <key>
// points the daemon at a local wrangler dev without touching the code.
