package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	serversURL       = "https://cfg.clianywhere.com/servers.json"
	serversCacheDir  = ".clianywhere"
	serversCacheFile = "servers.json"

	// probeTimeout shared deadline for the concurrent /health probe round:
	// all servers are probed in parallel with one 2s budget, so the whole
	// ranking phase is bounded by ~2s regardless of server count.
	probeTimeout = 2 * time.Second

	// location cache: ~/.clianywhere/location.cache, format "num|unix_seconds"
	locationCacheFile = "location.cache"
	locationTTL       = 24 * time.Hour

	// continent lookup endpoint (served by globalserver_worker)
	checkLocationURL = "https://globalserver.clianywhere.com/api/checklocation"

	// default continent number (fallback on miss/no-cache, corresponds to NA)
	defaultLocationNum = 5
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
// health = daemonCount / maxDaemonCount, i.e. load ratio; lower means more idle
type HealthResponse struct {
	Alive        bool    `json:"alive"`
	BrowserCount int     `json:"browser_count"`
	Health       float64 `json:"health"`
}

// cachePath returns the full path for the cached servers.json
func cachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, serversCacheDir, serversCacheFile), nil
}

// locationCachePath returns the full path for the location cache file
func locationCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, serversCacheDir, locationCacheFile), nil
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

	// validate: must be a JSON object mapping continent number (as string key) -> []ServerEntry
	var tmp map[string][]ServerEntry
	if err := json.Unmarshal(data, &tmp); err != nil {
		return nil, fmt.Errorf("invalid servers.json: %w", err)
	}

	if err := saveCache(data); err != nil {
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
func loadCachedServers() (map[string][]ServerEntry, error) {
	path, err := cachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cached servers.json failed: %w", err)
	}
	var servers map[string][]ServerEntry
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("parse cached servers.json failed: %w", err)
	}
	return servers, nil
}

// FetchServers downloads servers.json, falls back to cached copy on failure.
// Returns a map of continent number (string "1".."7") -> []ServerEntry.
func FetchServers(logger Logger) (map[string][]ServerEntry, error) {
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
			logger.Infof("[TS] using cached servers.json (%d regions)", len(cached))
		}
		return cached, nil
	}

	var servers map[string][]ServerEntry
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("parse servers.json failed: %w", err)
	}

	if logger != nil {
		logger.Infof("[TS] downloaded servers.json (%d regions)", len(servers))
	}
	return servers, nil
}

// ---- Location (continent) lookup: 24h local cache + checklocation endpoint ----

// loadLocationCache reads the local location cache, returns (num, unixSeconds, ok).
// ok=false means the file is missing or malformed.
func loadLocationCache() (num int, ts int64, ok bool) {
	path, err := locationCachePath()
	if err != nil {
		return 0, 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimSpace(string(data)), "|")
	if len(parts) != 2 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 || n > 7 {
		return 0, 0, false
	}
	t, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || t <= 0 {
		return 0, 0, false
	}
	return n, t, true
}

// saveLocationCache writes the location cache, format "num|unix_seconds"
func saveLocationCache(num int, ts int64) error {
	path, err := locationCachePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d|%d", num, ts)), 0600)
}

// fetchCheckLocation calls globalserver's /api/checklocation endpoint to get the continent number.
// Response format: {"c": N}, N in 1..7
func fetchCheckLocation() (int, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", checkLocationURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "CliAnyWhere/daemon")

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("checklocation request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("checklocation status: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	var payload struct {
		C int `json:"c"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("parse checklocation response failed: %w (body=%s)", err, string(body))
	}
	if payload.C < 1 || payload.C > 7 {
		return 0, fmt.Errorf("checklocation returned invalid number: %d", payload.C)
	}
	return payload.C, nil
}

// GetLocalInfo returns the continent number (1-7) of this machine.
// Uses the local cache first (valid for 24h); on expiry or absence, fetches via
// /api/checklocation and persists the result.
// On fetch failure: falls back to the expired cache; if no cache exists, returns 5 (NA).
func GetLocalInfo(logger Logger) (int, error) {
	// 1. local cache
	if cachedNum, cachedTs, ok := loadLocationCache(); ok {
		age := time.Since(time.Unix(cachedTs, 0))
		if age < locationTTL {
			return cachedNum, nil
		}
		if logger != nil {
			logger.Infof("[loc] cache expired (age=%s), refetching", age.Round(time.Second))
		}
	}

	// 2. fetch
	num, err := fetchCheckLocation()
	if err != nil {
		if logger != nil {
			logger.Warnf("[loc] checklocation failed: %v", err)
		}
		// fallback 1: expired cache
		if cachedNum, _, ok := loadLocationCache(); ok {
			if logger != nil {
				logger.Warnf("[loc] falling back to expired cache: %d", cachedNum)
			}
			return cachedNum, nil
		}
		// fallback 2: default NA
		if logger != nil {
			logger.Warnf("[loc] no cache available, defaulting to %d (NA)", defaultLocationNum)
		}
		return defaultLocationNum, nil
	}

	// 3. save
	if err := saveLocationCache(num, time.Now().Unix()); err != nil {
		if logger != nil {
			logger.Warnf("[loc] failed to save cache: %v", err)
		}
	}
	if logger != nil {
		logger.Infof("[loc] fetched location: %d", num)
	}
	return num, nil
}

// probeHealthOnce probes a single TS server's /health endpoint once.
// ctx carries the shared 2s budget of the whole probe round.
// Any failure (timeout, HTTP error, malformed response, not alive) yields 1.0
// (max load), so the server sinks to the bottom of the ranked list but is
// still eligible for connection — a dead health endpoint does not imply a
// dead WebSocket endpoint.
func probeHealthOnce(ctx context.Context, server ServerEntry, logger Logger) float64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.Health, nil)
	if err != nil {
		if logger != nil {
			logger.Warnf("[TS] probe %s: build request failed: %v", server.Addr, err)
		}
		return 1.0
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if logger != nil {
			logger.Warnf("[TS] probe %s failed: %v", server.Addr, err)
		}
		return 1.0
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if logger != nil {
			logger.Warnf("[TS] probe %s: read body failed: %v", server.Addr, err)
		}
		return 1.0
	}

	var hr HealthResponse
	if err := json.Unmarshal(body, &hr); err != nil {
		if logger != nil {
			logger.Warnf("[TS] probe %s: parse response failed: %v (body=%s)", server.Addr, err, string(body))
		}
		return 1.0
	}
	if !hr.Alive {
		if logger != nil {
			logger.Warnf("[TS] probe %s: not alive", server.Addr)
		}
		return 1.0
	}

	if logger != nil {
		logger.Debugf("[TS] probe %s: health=%.4f", server.Addr, hr.Health)
	}
	return hr.Health
}

// RankTurnServers full TS ranking flow, returns a connect-ordered server list
// plus the number of local-region servers at its head:
//  1. ForceTSAddr short-circuit (forced via config)
//  2. GetLocalInfo to get the continent number (1-7)
//  3. Fetch servers.json, flatten all regions and dedupe by Addr
//  4. Probe every server's /health concurrently under one shared 2s timeout;
//     failed probes score health=1.0 and sink to the bottom of their group
//  5. Sort: local-region servers first, then the rest; within each group by
//     health ascending (lower = more idle)
//
// The caller walks the list top-down trying to connect; localCount marks the
// retry boundary (local healthy servers get one retry, everything else one shot).
func RankTurnServers(logger Logger) ([]TurnServerEntry, int, error) {
	// ForceTSAddr short-circuit
	if cfg := loadForceTSAddr(); cfg != "" {
		if logger != nil {
			logger.Infof("[TS] ForceTSAddr set, using %s directly", cfg)
		}
		return []TurnServerEntry{{Addr: cfg}}, 1, nil
	}

	// 1. get continent number
	num, _ := GetLocalInfo(logger)
	regionKey := strconv.Itoa(num)

	// 2. fetch servers.json
	all, err := FetchServers(logger)
	if err != nil {
		return nil, 0, err
	}

	// 3. flatten all regions and dedupe by Addr
	// (the same machine may be listed under multiple region keys).
	// Go map iteration order is random, so the first key visited wins the
	// entry; when a later duplicate is found under our own region key, flip
	// the existing entry's local flag to true instead of skipping it.
	type flatEntry struct {
		ServerEntry
		local bool
	}
	seen := make(map[string]int) // addr -> index in flat
	var flat []flatEntry
	for region, servers := range all {
		for _, s := range servers {
			if s.Addr == "" {
				continue
			}
			if idx, ok := seen[s.Addr]; ok {
				if region == regionKey {
					flat[idx].local = true
				}
				continue
			}
			seen[s.Addr] = len(flat)
			flat = append(flat, flatEntry{ServerEntry: s, local: region == regionKey})
		}
	}
	if len(flat) == 0 {
		return nil, 0, fmt.Errorf("servers.json has no servers at all")
	}

	// 4. probe /health concurrently under one shared 2s budget
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	healths := make([]float64, len(flat))
	var wg sync.WaitGroup
	for i := range flat {
		wg.Add(1)
		go func(idx int, srv ServerEntry) {
			defer wg.Done()
			healths[idx] = probeHealthOnce(ctx, srv, logger)
		}(i, flat[i].ServerEntry)
	}
	wg.Wait()

	// 5. sort: local region first, then by health ascending within each group
	entries := make([]TurnServerEntry, len(flat))
	localCount := 0
	for i, e := range flat {
		entries[i] = TurnServerEntry{Addr: e.Addr, Health: healths[i], Local: e.local}
		if e.local {
			localCount++
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Local != entries[j].Local {
			return entries[i].Local
		}
		return entries[i].Health < entries[j].Health
	})

	if logger != nil {
		logger.Infof("[TS] ranked %d servers (region=%s local=%d):", len(entries), regionKey, localCount)
		for i, e := range entries {
			logger.Infof("[TS] #%d %s (health=%.4f local=%v)", i+1, e.Addr, e.Health, e.Local)
		}
	}

	return entries, localCount, nil
}
