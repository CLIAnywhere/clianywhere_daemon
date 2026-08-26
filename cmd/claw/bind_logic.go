package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"time"

	"github.com/gorilla/websocket"
)

var accessKeyRegex = regexp.MustCompile(`^[0-9a-zA-Z]+$`)

// BindCodeResult intermediate data for binding flow, for UI to display QR code
type BindCodeResult struct {
	BindCode   string
	QRPayload  []byte // JSON bytes for QR content
	TSWSURL    string // TurnServer WebSocket URL
	DeviceInfo string
}

// GenerateBindCode generate random bindcode and collect device info, for UI to display QR code
func GenerateBindCode(cfg *Config) (*BindCodeResult, error) {
	// generate 32-byte random bindcode (64 hex characters)
	bindcodeBytes := make([]byte, 32)
	if _, err := rand.Read(bindcodeBytes); err != nil {
		return nil, fmt.Errorf("failed to generate bindcode: %w", err)
	}
	bindcode := hex.EncodeToString(bindcodeBytes)

	// collect device info
	hostname, _ := os.Hostname()
	deviceInfo := fmt.Sprintf("%s|%s", hostname, runtime.GOOS)

	// get TurnServer (best-ranked server of the local region)
	ranked, _, err := RankTurnServers(nil)
	if err != nil || len(ranked) == 0 {
		return nil, fmt.Errorf("failed to get TurnServer: %w", err)
	}
	tsEntry := ranked[0]

	// construct QR content
	qrPayload, _ := json.Marshal(map[string]any{
		"b": bindcode,
		"a": tsEntry.Addr,
		"d": deviceInfo,
	})

	return &BindCodeResult{
		BindCode:   bindcode,
		QRPayload:  qrPayload,
		TSWSURL:    tsEntry.WSURL(),
		DeviceInfo: deviceInfo,
	}, nil
}

// ConnectAndSendBindCode connect to TurnServer and send bindcode
// return WebSocket connection for subsequent binding response wait
func ConnectAndSendBindCode(wsURL, bindcode string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		NetDial: func(network, addr string) (net.Conn, error) {
			c, err := net.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			if tc, ok := c.(*net.TCPConn); ok {
				tc.SetNoDelay(true)
			}
			return c, nil
		},
	}
	header := http.Header{}
	header.Set("User-Agent", "CliAnyWhere/daemon")
	header.Set("Cookie", "fromapp=clianywhere")

	conn, _, err := dialer.Dial(wsURL+"?role=daemon", header)
	if err != nil {
		return nil, fmt.Errorf("WebSocket connect failed: %w", err)
	}

	// send bindcode message
	bindMsg, _ := json.Marshal(map[string]any{
		"type": "bindcode",
		"data": bindcode,
	})
	if err := conn.WriteMessage(websocket.TextMessage, bindMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send bindcode failed: %w", err)
	}

	// wait for bindcode_ok (10s timeout)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("wait bindcode_ok failed: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	var resp map[string]any
	if err := json.Unmarshal(message, &resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("parse bindcode response failed: %w", err)
	}

	if resp["type"] == "bindcode_error" {
		conn.Close()
		msg, _ := resp["msg"].(string)
		return nil, fmt.Errorf("bindcode rejected: %s", msg)
	}

	if resp["type"] != "bindcode_ok" {
		conn.Close()
		return nil, fmt.Errorf("unexpected response: %s", string(message))
	}

	return conn, nil
}

// WaitForBindResponse wait for app binding response, return accesskey and device name
func WaitForBindResponse(conn *websocket.Conn, timeout time.Duration) (accesskey, deviceName string, err error) {
	conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return "", "", fmt.Errorf("connection closed or timed out (waited %v): %w", timeout, err)
		}

		var resp map[string]any
		if json.Unmarshal(message, &resp) != nil {
			continue
		}

		if resp["type"] == "dobindcode" {
			ak, _ := resp["accesskey"].(string)
			dn, _ := resp["deviceName"].(string)
			if ak == "" {
				return "", "", fmt.Errorf("dobindcode missing accesskey")
			}
			return ak, dn, nil
		}
	}
}
