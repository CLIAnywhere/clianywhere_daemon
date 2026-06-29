package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	serversURL      = "https://cfg.clianywhere.com/servers.json"
	serversCacheDir = ".clianywhere"
	serversCacheFile = "servers.json"
	healthThreshold = 0.99
)

// forceTSAddr package-level override, set from Config.ForceTSAddr at startup
var forceTSAddr string

// SetForceTSAddr set the forced TS address (called once at startup)
func SetForceTSAddr(addr string) {
	forceTSAddr = addr
}

// loadForceTSAddr returns the forced TS address if set
func loadForceTSAddr() string {
	return forceTSAddr
}

// ServerEntry from servers.json
type ServerEntry struct {
	Addr   string `json:"addr"`
	Health string `json:"health"`
}

// HealthResponse from TS /health endpoint
type HealthResponse struct {
	Alive        bool    `json:"alive"`
	BrowserCount int     `json:"browser_count"`
	Health       float64 `json:"health"`
}

// probeResult latency probe result for one TS
type probeResult struct {
	server  ServerEntry
	latency time.Duration // second call latency
	health  float64
	alive   bool
	err     error
}

// cachePath returns the full path for the cached servers.json
func cachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, serversCacheDir, serversCacheFile), nil
}

// downloadServers downloads servers.json from remote and saves to cache file.
// Returns the raw bytes. Caller handles errors / fallback.
func downloadServers() ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(serversURL)
	if err != nil {
		return nil, fmt.Errorf("download servers.json failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download servers.json status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read servers.json failed: %w", err)
	}

	// validate it's valid JSON array
	var tmp []ServerEntry
	if err := json.Unmarshal(data, &tmp); err != nil {
		return nil, fmt.Errorf("invalid servers.json: %w", err)
	}

	// save to cache
	if err := saveCache(data); err != nil {
		// non-fatal, just skip cache write
		_ = err
	}

	return data, nil
}

// saveCache writes data to the local cache file
func saveCache(data []byte) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0755)
	return os.WriteFile(path, data, 0644)
}

// loadCachedServers reads servers.json from local cache file
func loadCachedServers() ([]ServerEntry, error) {
	path, err := cachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cached servers.json failed: %w", err)
	}
	var servers []ServerEntry
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("parse cached servers.json failed: %w", err)
	}
	return servers, nil
}

// FetchServers downloads servers.json, falls back to cached copy on failure
func FetchServers(logger Logger) ([]ServerEntry, error) {
	data, err := downloadServers()
	if err != nil {
		if logger != nil {
			logger.Warnf("[TS] download servers.json failed: %v, trying cache", err)
		}
		cached, cerr := loadCachedServers()
		if cerr != nil {
			return nil, fmt.Errorf("download failed (%v) and cache unavailable (%v)", err, cerr)
		}
		if logger != nil {
			logger.Infof("[TS] using cached servers.json (%d servers)", len(cached))
		}
		return cached, nil
	}

	var servers []ServerEntry
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("parse servers.json failed: %w", err)
	}

	if logger != nil {
		logger.Infof("[TS] downloaded servers.json (%d servers)", len(servers))
	}
	return servers, nil
}

// probeServer probes a single TS server: call health URL twice, record second call latency
func probeServer(server ServerEntry, logger Logger) probeResult {
	result := probeResult{server: server}
	addr := server.Addr

	client := &http.Client{Timeout: 5 * time.Second}

	// first call — warm up DNS, discard timing
	if logger != nil {
		logger.Debugf("[TS] probing %s (warmup request)", addr)
	}
	resp, err := client.Get(server.Health)
	if err != nil {
		result.err = fmt.Errorf("first health check failed: %w", err)
		if logger != nil {
			logger.Warnf("[TS] %s warmup request failed: %v", addr, err)
		}
		return result
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	// second call — measure latency
	if logger != nil {
		logger.Debugf("[TS] probing %s (measuring latency)", addr)
	}
	start := time.Now()
	resp, err = client.Get(server.Health)
	if err != nil {
		result.err = fmt.Errorf("second health check failed: %w", err)
		if logger != nil {
			logger.Warnf("[TS] %s latency request failed: %v", addr, err)
		}
		return result
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	result.latency = time.Since(start)

	// parse health response
	var hr HealthResponse
	if err := json.Unmarshal(body, &hr); err != nil {
		result.err = fmt.Errorf("parse health response failed: %w", err)
		return result
	}

	result.alive = hr.Alive
	result.health = hr.Health

	if logger != nil {
		logger.Infof("[TS] %s latency=%dms health=%.4f alive=%v browsers=%d",
			addr, result.latency.Milliseconds(), result.health, result.alive, hr.BrowserCount)
	}

	return result
}

// SelectBestTurnServer fetches server list, probes each concurrently,
// returns the server with lowest latency among healthy ones (health < 0.99)
func SelectBestTurnServer(logger Logger) (*TurnServerEntry, error) {
	// if ForceTSAddr is set, skip selection and use it directly
	cfg := loadForceTSAddr()
	if cfg != "" {
		if logger != nil {
			logger.Infof("[TS] ForceTSAddr set, using %s directly", cfg)
		}
		return &TurnServerEntry{Addr: cfg}, nil
	}

	servers, err := FetchServers(logger)
	if err != nil {
		return nil, err
	}

	if len(servers) == 0 {
		return nil, fmt.Errorf("no servers available")
	}

	if len(servers) == 1 {
		// single server, just probe to check health
		r := probeServer(servers[0], logger)
		if r.err != nil {
			return nil, fmt.Errorf("server %s health check failed: %v", servers[0].Addr, r.err)
		}
		if !r.alive || r.health >= healthThreshold {
			return nil, fmt.Errorf("server %s unhealthy (alive=%v health=%.4f)", servers[0].Addr, r.alive, r.health)
		}
		if logger != nil {
			logger.Infof("[TS] selected %s (only server, latency=%dms, health=%.4f)", servers[0].Addr, r.latency.Milliseconds(), r.health)
		}
		return &TurnServerEntry{Addr: servers[0].Addr}, nil
	}

	// probe all servers concurrently
	results := make([]probeResult, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func(idx int, srv ServerEntry) {
			defer wg.Done()
			results[idx] = probeServer(srv, logger)
		}(i, s)
	}
	wg.Wait()

	// find best: alive && health < 0.99 && lowest latency
	var best *probeResult
	for i := range results {
		r := &results[i]
		if r.err != nil {
			if logger != nil {
				logger.Warnf("[TS] server %s probe failed: %v", r.server.Addr, r.err)
			}
			continue
		}
		if !r.alive {
			if logger != nil {
				logger.Warnf("[TS] server %s not alive", r.server.Addr)
			}
			continue
		}
		if r.health >= healthThreshold {
			if logger != nil {
				logger.Warnf("[TS] server %s health=%.4f >= %.2f, skipping", r.server.Addr, r.health, healthThreshold)
			}
			continue
		}
		if best == nil || r.latency < best.latency {
			best = r
		}
	}

	if best == nil {
		// fallback: no server passed all filters, pick one with lowest health
		for i := range results {
			r := &results[i]
			if r.err != nil {
				continue
			}
			if best == nil || r.health < best.health {
				best = r
			}
		}
		if best != nil {
			if logger != nil {
				logger.Warnf("[TS] no healthy server found, fallback to %s (health=%.4f)", best.server.Addr, best.health)
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no server available among %d candidates (all probes failed)", len(servers))
	}

	if logger != nil {
		logger.Infof("[TS] selected %s (latency=%dms, health=%.4f) from %d candidates",
			best.server.Addr, best.latency.Milliseconds(), best.health, len(servers))
	}

	return &TurnServerEntry{Addr: best.server.Addr}, nil
}
