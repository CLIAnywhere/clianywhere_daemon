package main

import (
	"os"
	"time"
)

// Config daemon configuration
type Config struct {
	GlobalServerURL string
	PollInterval    time.Duration
	PoolSize        int
	DefaultShell    string
	DefaultCols     int
	DefaultRows     int
	HistoryLines    int // VT100 scrollback max lines
	ForceTS         bool // disable P2P, force TS relay
	ForceTSAddr     string // skip TS selection, directly use this TS addr (full WebSocket URL)

	// file transfer
	IPCHTTPPort int // HTTP IPC port (for daemon send), default 19876
	ChunkSize   int // file chunk size (bytes), 0 = auto (MaxFrameSize-9)
}

// DefaultConfig default configuration
func DefaultConfig() *Config {
	return &Config{
		ForceTS:     os.Getenv("CLIANYWHERE_NOP2P") == "1",
		GlobalServerURL: "https://globalserver.clianywhere.com",
		PollInterval:    3 * time.Second,
		PoolSize:        1,
		DefaultShell:    defaultShellName(), // auto-resolves at runtime
		DefaultCols:     120,
		DefaultRows:     36,
		HistoryLines:    1000, // 1000 lines scrollback
		IPCHTTPPort:     56881,
		ChunkSize:       0, // 0 = auto (MaxFrameSize-9)
	}
}
