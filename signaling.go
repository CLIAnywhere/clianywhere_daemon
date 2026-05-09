package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SignalingClient globalserver HTTP client (only for getting TurnServer info)
type SignalingClient struct {
	baseURL    string
	httpClient *http.Client
	logger     Logger
}

func NewSignalingClient(baseURL string, logger Logger) *SignalingClient {
	return &SignalingClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

// ========== DATA STRUCTURES ==========

// TurnServerEntry TS 地址信息，Addr 为完整 WebSocket URL（如 wss://abc:port 或 ws://abc:port）
type TurnServerEntry struct {
	Addr string
}

// WSURL 返回完整 WebSocket 连接地址（直接使用 TS 上报的 addr）
func (e TurnServerEntry) WSURL() string {
	return e.Addr
}

// ========== TurnServer METHODS ==========

// GetTurnServer call getturnserver API, Worker selects best TS based on continent and load
func (sc *SignalingClient) GetTurnServer() (*TurnServerEntry, error) {
	resp, err := sc.get("/api/turn/getturnserver")
	if err != nil {
		return nil, err
	}

	code, _ := resp["code"].(float64)
	if code != 0 {
		return nil, fmt.Errorf("getturnserver error: %v", resp["msg"])
	}

	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return nil, nil
	}

	tsRaw, _ := data["turn_server"].(map[string]any)
	if tsRaw == nil {
		return nil, nil
	}

	entry := &TurnServerEntry{}
	if v, ok := tsRaw["addr"].(string); ok {
		entry.Addr = v
	}
	return entry, nil
}

// ========== HTTP HELPERS ==========

func (sc *SignalingClient) get(path string) (map[string]any, error) {
	req, err := http.NewRequest("GET", sc.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CliAnyWhere/daemon")
	req.Header.Set("Cookie", "fromapp=clianywhere")

	resp, err := sc.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return parseResponse(resp.Body, sc.logger)
}

func parseResponse(body io.Reader, logger Logger) (map[string]any, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		if logger != nil {
			logDebugf(logger, "[signaling]", "response is not JSON: %s", string(data))
		}
		return nil, fmt.Errorf("parse response failed: %w (body: %s)", err, string(data))
	}
	return result, nil
}
