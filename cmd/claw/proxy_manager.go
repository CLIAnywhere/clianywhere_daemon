package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsDialer skips TLS verification for WSS connections to self-signed / IP-address targets.
var wsDialer = &websocket.Dialer{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}

// ProxyManager manage all proxy tunnel connections
type ProxyManager struct {
	mu         sync.Mutex
	conns      map[uint32]*ProxyConn    // connId → active TCP proxy connection
	wsConns    map[string]*ProxyWsConn  // sessionID → active WS proxy connection
	daemon     *Daemon
	logger     Logger
	httpClient *http.Client // persistent client with cookie jar for proxy_http_fetch
}

// ProxyConn single proxy TCP connection
type ProxyConn struct {
	ID         uint32
	ConnID     string // "conn_00000001" format, maps to JSON session_id
	tcpConn    net.Conn
	daemon     *Daemon
	cancel     chan struct{}
	once       sync.Once
	remoteAddr string
}

// ProxyWsConn single proxy WebSocket connection
type ProxyWsConn struct {
	ConnID string
	wsConn *websocket.Conn
	daemon *Daemon
	cancel chan struct{}
	once   sync.Once
}

// NewProxyManager create proxy manager
func NewProxyManager(d *Daemon, logger Logger) *ProxyManager {
	jar, _ := cookiejar.New(nil)
	return &ProxyManager{
		daemon:  d,
		logger:  logger,
		conns:   make(map[uint32]*ProxyConn),
		wsConns: make(map[string]*ProxyWsConn),
		httpClient: &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			Timeout: 30 * time.Second,
		},
	}
}

// HandleConnect handle proxy_connect message: asynchronously establish TCP connection to target
// note: TCP dial executes in goroutine, does not block caller (P2P DataChannel OnMessage)
func (pm *ProxyManager) HandleConnect(msg *Message) {
	connIDStr := msg.SessionID
	targetAddr := msg.Data // "host:port"


	// extract uint32 connId from session_id
	var connID uint32
	fmt.Sscanf(connIDStr, "conn_%08d", &connID)
	if connID == 0 {
		pm.daemon.sendJSON(&Message{
			Type:      TypeProxyError,
			SessionID: connIDStr,
			Error:     "invalid connection id",
		})
		return
	}

	// goroutine: async TCP dial, does not block message processing loop
	go func() {
		tcpConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
		if err != nil {
			pm.daemon.sendJSON(&Message{
				Type:      TypeProxyError,
				SessionID: connIDStr,
				Error:     err.Error(),
			})
			return
		}

		// register connection
		pc := &ProxyConn{
			ID:         connID,
			ConnID:     connIDStr,
			tcpConn:    tcpConn,
			daemon:     pm.daemon,
			cancel:     make(chan struct{}),
			remoteAddr: tcpConn.RemoteAddr().String(),
		}

		pm.mu.Lock()
		pm.conns[connID] = pc
		pm.mu.Unlock()

		// notify app connection success
		pm.daemon.sendJSON(&Message{
			Type:      TypeProxyConnected,
			SessionID: connIDStr,
		})


		// start readLoop: read data from TCP connection and send to app
		pm.readLoop(pc)
	}()
}

// readLoop continuously read data from TCP connection, construct binary frames and send to app
func (pm *ProxyManager) readLoop(pc *ProxyConn) {
	buf := make([]byte, MaxFrameSize-5) // 5-byte proxy data header
	defer pm.removeConn(pc.ID)

	for {
		select {
		case <-pc.cancel:
			return
		default:
		}

		n, err := pc.tcpConn.Read(buf)
		if n > 0 {
			frameSize := 5 + n
			if frameSize > 31*1024 {
			}
			// construct binary frame: [0x05][4B connId BE][payload...]
			frame := make([]byte, 5+n)
			frame[0] = OpcodeProxyData
			binary.BigEndian.PutUint32(frame[1:5], pc.ID)
			copy(frame[5:], buf[:n])
			pc.daemon.sendBytes(frame)
		}
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
			} else if n == 0 {
			} else {
			}
			// notify app connection closed
			pc.daemon.sendJSON(&Message{
				Type:      TypeProxyClose,
				SessionID: pc.ConnID,
			})
			return
		}
	}
}

// HandleData handle tunnel data frame from app (header stripped)
func (pm *ProxyManager) HandleData(connID uint32, data []byte) {
	pm.mu.Lock()
	pc, ok := pm.conns[connID]
	pm.mu.Unlock()

	if !ok {
		return
	}

	_, err := pc.tcpConn.Write(data)
	if err != nil {
		pm.removeConn(connID)
	}
}

// HandleClose handle proxy_close message
func (pm *ProxyManager) HandleClose(msg *Message) {
	connIDStr := msg.SessionID
	var connID uint32
	fmt.Sscanf(connIDStr, "conn_%08d", &connID)

	pm.removeConn(connID)
}

// removeConn remove and close connection
func (pm *ProxyManager) removeConn(connID uint32) {
	pm.mu.Lock()
	pc, ok := pm.conns[connID]
	if ok {
		delete(pm.conns, connID)
	}
	pm.mu.Unlock()

	if ok {
		pc.once.Do(func() {
			close(pc.cancel)
			pc.tcpConn.Close()
		})
	}
}

// CloseAll close all active proxy connections (called when channel disconnects)
func (pm *ProxyManager) CloseAll() {
	pm.mu.Lock()
	conns := make([]*ProxyConn, 0, len(pm.conns))
	for _, pc := range pm.conns {
		conns = append(conns, pc)
	}
	pm.conns = make(map[uint32]*ProxyConn)

	wsConns := make([]*ProxyWsConn, 0, len(pm.wsConns))
	for _, wc := range pm.wsConns {
		wsConns = append(wsConns, wc)
	}
	pm.wsConns = make(map[string]*ProxyWsConn)
	pm.mu.Unlock()

	for _, pc := range conns {
		pc.once.Do(func() {
			close(pc.cancel)
			pc.tcpConn.Close()
		})
	}
	for _, wc := range wsConns {
		wc.once.Do(func() {
			close(wc.cancel)
			wc.wsConn.Close()
		})
	}
}

// HandleHttpFetch handles proxy_http_fetch: makes an HTTP request to the target URL
// and returns the response to the app. Used by the Web SW-based proxy.
func (pm *ProxyManager) HandleHttpFetch(msg *Message) {
	connIDStr := msg.SessionID
	targetURL := msg.Data // full URL
	method := msg.Method
	if method == "" {
		method = "GET"
	}

	go func() {
		// Build request
		var bodyReader io.Reader
		if msg.BodyBase64 != "" {
			bodyBytes, err := base64.StdEncoding.DecodeString(msg.BodyBase64)
			if err == nil && len(bodyBytes) > 0 {
				bodyReader = strings.NewReader(string(bodyBytes))
			}
		}

		req, err := http.NewRequest(method, targetURL, bodyReader)
		if err != nil {
			pm.daemon.sendJSON(&Message{
				Type:      TypeProxyError,
				SessionID: connIDStr,
				Error:     err.Error(),
			})
			return
		}

		// Set headers from request
		if msg.HeadersJSON != "" && msg.HeadersJSON != "{}" {
			var headers map[string]string
			if err := json.Unmarshal([]byte(msg.HeadersJSON), &headers); err == nil {
				for k, v := range headers {
					// Skip hop-by-hop headers
					if strings.EqualFold(k, "host") {
						continue
					}
					req.Header.Set(k, v)
				}
			}
		}

		// Set Host header from URL
		if req.Header.Get("Host") == "" {
			req.Header.Set("Host", req.URL.Host)
		}

		// Make HTTP request using persistent client (with cookie jar for session persistence)
		resp, err := pm.httpClient.Do(req)
		if err != nil {
			pm.daemon.sendJSON(&Message{
				Type:      TypeProxyError,
				SessionID: connIDStr,
				Error:     err.Error(),
			})
			return
		}
		defer resp.Body.Close()

		// Read body (limit 10 MB)
		body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
		if err != nil {
			pm.daemon.sendJSON(&Message{
				Type:      TypeProxyError,
				SessionID: connIDStr,
				Error:     err.Error(),
			})
			return
		}

		// Build response headers
		headers := make(map[string]string)
		for k, v := range resp.Header {
			if len(v) > 0 {
				headers[k] = v[0]
			}
		}
		headers["Content-Length"] = fmt.Sprintf("%d", len(body))
		headersJSON, _ := json.Marshal(headers)

		// Base64 encode and chunk to fit MaxFrameSize (31KB).
		// Reserve ~8KB for JSON envelope overhead → 20KB per base64 chunk.
		bodyB64 := base64.StdEncoding.EncodeToString(body)
		const chunkSize = 20 * 1024
		totalLen := len(bodyB64)
		totalChunks := (totalLen + chunkSize - 1) / chunkSize
		if totalChunks == 0 {
			totalChunks = 1
		}

		for i := 0; i < totalChunks; i++ {
			start := i * chunkSize
			end := start + chunkSize
			if end > totalLen {
				end = totalLen
			}
			pm.daemon.sendJSON(&Message{
				Type:        TypeProxyHttpResponse,
				SessionID:   connIDStr,
				StatusCode:  resp.StatusCode,     // meaningful only on chunk 0
				StatusText:  resp.Status,         // meaningful only on chunk 0
				HeadersJSON: string(headersJSON), // meaningful only on chunk 0
				Data:        bodyB64[start:end],
				ChunkIndex:  i,
				TotalChunks: totalChunks,
			})
		}
	}()
}

// ─── WebSocket proxy ───

// HandleWsConnect handles proxy_ws_connect: dials target WebSocket server
func (pm *ProxyManager) HandleWsConnect(msg *Message) {
	connIDStr := msg.SessionID
	targetURL := msg.Data
	pm.logger.Infof("WS-PROXY connect: %s -> %s", connIDStr, targetURL)

	go func() {
		// Build request header with cookies from the persistent jar
		header := http.Header{}
		if u, err := url.Parse(targetURL); err == nil {
			// Convert wss:// to https:// for cookie lookup
			httpURL := "https://" + u.Host + u.Path
			if hu, e := url.Parse(httpURL); e == nil {
				for _, c := range pm.httpClient.Jar.Cookies(hu) {
					header.Add("Cookie", c.String())
				}
			}
		}
		// Set Origin to target (some servers check Origin)
		if strings.HasPrefix(targetURL, "wss://") {
			header.Set("Origin", "https://"+strings.TrimPrefix(strings.SplitN(targetURL, "/", 4)[2], "wss://"))
		} else {
			header.Set("Origin", "http://"+strings.TrimPrefix(strings.SplitN(targetURL, "/", 4)[2], "ws://"))
		}

		conn, _, err := wsDialer.Dial(targetURL, header)
		if err != nil {
			pm.logger.Infof("WS-PROXY dial fail: %s %s", connIDStr, err.Error())
			pm.daemon.sendJSON(&Message{
				Type:      TypeProxyWsError,
				SessionID: connIDStr,
				Error:     err.Error(),
			})
			return
		}

		wc := &ProxyWsConn{
			ConnID: connIDStr,
			wsConn: conn,
			daemon: pm.daemon,
			cancel: make(chan struct{}),
		}

		pm.mu.Lock()
		pm.wsConns[connIDStr] = wc
		pm.mu.Unlock()

		pm.logger.Infof("WS-PROXY connected: %s", connIDStr)
		// Notify app: WS connected
		pm.daemon.sendJSON(&Message{
			Type:      TypeProxyWsConnected,
			SessionID: connIDStr,
		})

		pm.readWsLoop(wc)
	}()
}

// readWsLoop continuously reads messages from the WebSocket connection
func (pm *ProxyManager) readWsLoop(wc *ProxyWsConn) {
	defer pm.removeWsConn(wc.ConnID)

	for {
		select {
		case <-wc.cancel:
			return
		default:
		}

		msgType, data, err := wc.wsConn.ReadMessage()
		if err != nil {
			// Connection closed or error
			wc.daemon.sendJSON(&Message{
				Type:      TypeProxyWsClose,
				SessionID: wc.ConnID,
			})
			return
		}

		isBinary := msgType == websocket.BinaryMessage
		encoded := base64.StdEncoding.EncodeToString(data)

		wc.daemon.sendJSON(&Message{
			Type:      TypeProxyWsMessage,
			SessionID: wc.ConnID,
			Data:      encoded,
			IsBinary:  isBinary,
		})
	}
}

// HandleWsMessage handles proxy_ws_message: writes data to the WS connection
func (pm *ProxyManager) HandleWsMessage(msg *Message) {
	pm.mu.Lock()
	wc, ok := pm.wsConns[msg.SessionID]
	pm.mu.Unlock()

	if !ok {
		return
	}

	data, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		return
	}

	msgType := websocket.TextMessage
	if msg.IsBinary {
		msgType = websocket.BinaryMessage
	}

	if err := wc.wsConn.WriteMessage(msgType, data); err != nil {
		pm.removeWsConn(msg.SessionID)
	}
}

// HandleWsClose handles proxy_ws_close: closes the WS connection
func (pm *ProxyManager) HandleWsClose(msg *Message) {
	pm.removeWsConn(msg.SessionID)
}

// removeWsConn removes and closes a WS connection
func (pm *ProxyManager) removeWsConn(connID string) {
	pm.mu.Lock()
	wc, ok := pm.wsConns[connID]
	if ok {
		delete(pm.wsConns, connID)
	}
	pm.mu.Unlock()

	if ok {
		wc.once.Do(func() {
			close(wc.cancel)
			wc.wsConn.Close()
		})
	}
}
