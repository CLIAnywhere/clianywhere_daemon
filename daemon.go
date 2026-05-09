package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v3"
)

// Daemon main daemon process — coordinates PTY management and single-channel communication (WebRTC P2P or TS WebSocket, one at a time)
// channelMode empty string defaults to TS (compat), browser explicitly selects channel via channel_select message
type Daemon struct {
	accessKey string
	cfg       *Config
	logger    Logger
	ptyMgr    *PTYManager
	signaling *SignalingClient

	rtc   *WebRTCConn
	rtcMu sync.RWMutex

	// Trickle ICE: buffer ICE candidates that arrive before rtc is created
	pendingICE     []map[string]any
	pendingICEMode bool // true means buffering (startP2PAsAnswerer in progress)

	wsRelay *WSTurnRelay
	wsMu    sync.RWMutex

	channelMode string // "p2p" or "ts", empty string means not selected (defaults to TS)
	channelMu   sync.RWMutex

	// cached public IP, fallback when STUN fails
	cachedPublicIP string
	publicIPMu     sync.RWMutex

	// mutual-kick flag: set to 1 when kicked is received, blocks ongoing P2P establishment
	kicked int32

	// set of sessions subscribed by frontend: only sessions that went through request_history receive real-time output
	subscribed map[string]bool
	subMu      sync.RWMutex

	// file transfer
	fileTransfer *FileTransferManager

	// HTTP proxy tunnel
	proxyManager *ProxyManager

	ipcServer *http.Server

	// connection state notification (for GUI; CLI version uses nil, no impact)
	OnStateChange func(state string) // "stopped", "connecting", "connected"

	// connection state (atomic, for status command query)
	connState atomic.Value // string: "stopped", "connecting", "connected"

	// stop signal: Stop() closes this channel, startTSRelay exits loop on detection
	stopCh    chan struct{}
	stopOnce  sync.Once

	// local WebSocket service (local attach)
	localServer *LocalServer
}

// NewDaemon create daemon instance
func NewDaemon(accessKey string, cfg *Config, logger Logger) *Daemon {
	return &Daemon{
		accessKey:  accessKey,
		cfg:        cfg,
		logger:     logger,
		subscribed: make(map[string]bool),
		stopCh:     make(chan struct{}),
	}
}

// Init initialize local resources（does not depend on accessKey, called once at startup）
// creates PTY pool, local WS, IPC and other local services, then daemon attach is available
func (d *Daemon) Init() {
	// 1. initialize PTY manager
	d.ptyMgr = NewPTYManager(d.cfg, d.logger)

	d.ptyMgr.OnData = func(sessionID string, data []byte, seq uint64) {
		d.sendOutput(sessionID, string(data), seq)
	}

	d.ptyMgr.OnExit = func(sessionID string, exitCode int) {
		d.subMu.Lock()
		delete(d.subscribed, sessionID)
		d.subMu.Unlock()

		d.sendJSON(&Message{
			Type:      TypeSessionExited,
			SessionID: sessionID,
			ExitCode:  exitCode,
		})
		d.replenishPool()
	}

	// 2. create initial session pool
	pool := d.ptyMgr.CreatePool(d.cfg.PoolSize, d.cfg.DefaultShell)
	logPrintf(d.logger, "[daemon]", "created pool of %d sessions", len(pool))
	for _, s := range pool {
		logPrintf(d.logger, "[daemon]", "pool session %s (pid=%d)", s.ID, s.PID)
	}

	// 3. initialize file transfer manager
	d.fileTransfer = NewFileTransferManager(d, d.logger)

	// 4. initialize HTTP proxy tunnel manager
	d.proxyManager = NewProxyManager(d, d.logger)

	// 5. start HTTP IPC server (for daemon send)
	d.startIPCServer()

	// 6. start local WebSocket service (for local attach)
	d.localServer = StartLocalServer(d)
}

// StartRemote start remote connection with accessKey (signaling + TS relay)
// can be called at any time after Init(), supports hot-connect (e.g. called after binding completes)
func (d *Daemon) StartRemote(accessKey string) {
	d.accessKey = accessKey

	// initialize signaling client
	d.signaling = NewSignalingClient(d.cfg.GlobalServerURL, d.logger)

	// start TurnServer WebSocket connection (auto-selects best in background + auto-reconnect)
	d.startTSRelay()

	logPrintf(d.logger, "[daemon]", "accesskey=%s server=%s forcets=%v", accessKey, d.cfg.GlobalServerURL, d.cfg.ForceTS)
}

// handleP2PSignal handle P2P signaling messages from TS WebSocket
func (d *Daemon) handleP2PSignal(raw map[string]any) {
	msgType, _ := raw["type"].(string)

	// forcets mode: silently discard P2P signaling, let frontend timeout and fallback
	if d.cfg.ForceTS {
		logDebugf(d.logger, "[CHAN]", "P2P signaling %s discarded (forcets)", msgType)
		return
	}

	switch msgType {
	case "p2p_offer":
		sdpOffer, _ := raw["sdp"].(string)
		if sdpOffer == "" {
			logPrintf(d.logger, "[daemon]", "received p2p_offer but SDP is empty")
			return
		}
		d.rtcMu.Lock()
		d.pendingICE = nil
		d.pendingICEMode = true
		d.rtcMu.Unlock()
		go d.startP2PAsAnswerer(sdpOffer)

	case "peer_offline":
		logPrintf(d.logger, "[daemon]", "received peer_offline (via P2P signal)")
		d.handlePeerOffline()

	case "p2p_ice":
		logDebugf(d.logger, "[P2P-DEBUG-DA]", "recv p2p_ice from browser")
		d.handleP2PICE(raw)
	}
}

// startP2PAsAnswerer after receiving p2p_offer from browser, create WebRTC answerer
func (d *Daemon) startP2PAsAnswerer(sdpOffer string) {
	// new frontend sent p2p_offer, old connection is done, clearing kicked flag
	atomic.StoreInt32(&d.kicked, 0)

	// close old P2P connection
	d.CloseRTC()

	startTime := time.Now()
	logDebugf(d.logger, "[P2P-DEBUG-DA]", "=== recv p2p_offer, start answerer ===")

	// use WebRTCConn answerer mode
	rtc := NewWebRTCConn(d.accessKey, nil, d.cfg, d.logger) // signaling=nil, no HTTP

	// ICE candidates sent via TS WebSocket
	rtc.OnICECandidateFunc = func(candidate, sdpMid string, sdpMLineIndex int) {
		short := candidate
		if len(short) > 60 {
			short = short[:60]
		}
		logDebugf(d.logger, "[P2P-DEBUG-DA]", "local ICE: %s +%v", short, time.Since(startTime))
		data, _ := json.Marshal(map[string]any{
			"type":            "p2p_ice",
			"candidate":       candidate,
			"sdp_mid":         sdpMid,
			"sdp_mline_index": sdpMLineIndex,
		})
		d.wsSendRaw(data)
	}

	// terminal I/O message callback
	rtc.OnMessage = func(msg *Message) {
		d.handleMessage(msg, "P2P")
	}

	// binary frame callback
	rtc.OnBinaryMessage = func(data []byte) {
		d.handleBinary(data)
	}

	// use multiple STUN servers to increase candidate diversity and improve P2P success rate
	iceServers := []webrtc.ICEServer{
		{URLs: []string{
			"stun:stun.qq.com:3478",
			"stun:stun.cloudflare.com:3478",
			"stun:stun.l.google.com:19302",
			"stun:stun1.l.google.com:19302",
		}},
	}

	// create answer
	answer, err := rtc.Answer(sdpOffer, iceServers)
	if err != nil {
		logErrorf(d.logger, "[daemon]", "P2P answer failed: %v", err)
		return
	}
	logDebugf(d.logger, "[P2P-DEBUG-DA]", "answer created +%v", time.Since(startTime))

	// send answer back to browser (via TS)
	logDebugf(d.logger, "[P2P-DEBUG-DA]", "send p2p_answer +%v", time.Since(startTime))
	d.wsSendRaw(map[string]any{
		"type":       "p2p_answer",
		"sdp_answer": answer,
	})

	// set rtc and replay buffered ICE candidates
	d.rtcMu.Lock()
	d.rtc = rtc
	pending := d.pendingICE
	d.pendingICE = nil
	d.pendingICEMode = false
	d.rtcMu.Unlock()

	for _, iceRaw := range pending {
		candidate, _ := iceRaw["candidate"].(string)
		sdpMid, _ := iceRaw["sdp_mid"].(string)
		sdpMLineIndex := 0
		if v, ok := iceRaw["sdp_mline_index"].(float64); ok {
			sdpMLineIndex = int(v)
		}
		if err := rtc.AddRemoteICE(candidate, sdpMid, sdpMLineIndex); err != nil {
			logErrorf(d.logger, "[daemon]", "replay buffered ICE failed: %v", err)
		}
	}
	if len(pending) > 0 {
		logPrintf(d.logger, "[daemon]", "replayed %d buffered ICE candidates", len(pending))
	}

	logPrintf(d.logger, "[daemon]", "P2P answer sent, waiting for connection...")

	// waiting for connection to establish (10s timeout)
	if err := rtc.WaitConnected(10 * time.Second); err != nil {
		logErrorf(d.logger, "[daemon]", "P2P connection timeout: %v", err)
		rtc.Close()
		d.rtcMu.Lock()
		if d.rtc == rtc {
			d.rtc = nil
		}
		d.rtcMu.Unlock()
		return
	}

	logPrintf(d.logger, "[daemon]", "P2P connected, waiting for browser channel_select")

	// block until P2P disconnects
	rtc.WaitDisconnected()

	// P2P disconnected
	d.rtcMu.Lock()
	if d.rtc == rtc {
		d.rtc = nil
	} else {
		// d.rtc taken over by new connection, old goroutine does not interfere
		d.rtcMu.Unlock()
		rtc.Close()
		logPrintf(d.logger, "[daemon]", "P2P disconnected (taken over by new connection)")
		return
	}
	d.rtcMu.Unlock()

	// if current mode is p2p, notify browser channel is down
	d.channelMu.RLock()
	mode := d.channelMode
	d.channelMu.RUnlock()
	if mode == "p2p" {
		d.channelMu.Lock()
		d.channelMode = "" // reset, waiting for browser to re-select
		d.channelMu.Unlock()
		// notify browser via TS
		d.wsSendRaw(map[string]any{
			"type":    "channel_failed",
			"channel": "p2p",
		})
		logPrintf(d.logger, "[daemon]", "P2P channel down, browser notified")
	}

	rtc.Close()
	logPrintf(d.logger, "[daemon]", "P2P disconnected")

	// P2P disconnected, cancel all in-progress file transfers
	if d.fileTransfer != nil {
		d.fileTransfer.CancelAll()
	}
	if d.proxyManager != nil {
		d.proxyManager.CloseAll()
	}

	// clear subscriptions, stop PTY output
	d.subMu.Lock()
	d.subscribed = make(map[string]bool)
	d.subMu.Unlock()
}

// handleP2PICE handle ICE candidates from browser
func (d *Daemon) handleP2PICE(raw map[string]any) {
	candidate, _ := raw["candidate"].(string)
	sdpMid, _ := raw["sdp_mid"].(string)
	sdpMLineIndex := 0
	if v, ok := raw["sdp_mline_index"].(float64); ok {
		sdpMLineIndex = int(v)
	}

	d.rtcMu.Lock()
	if d.pendingICEMode && d.rtc == nil {
		// rtc not yet created, buffering candidate
		d.pendingICE = append(d.pendingICE, raw)
		d.rtcMu.Unlock()
		return
	}
	rtc := d.rtc
	d.rtcMu.Unlock()

	if rtc != nil {
		if err := rtc.AddRemoteICE(candidate, sdpMid, sdpMLineIndex); err != nil {
			logErrorf(d.logger, "[daemon]", "add remote ICE failed: %v", err)
		}
	}
}

// wsSendRaw send raw JSON via TS WebSocket
func (d *Daemon) wsSendRaw(v any) {
	var data []byte
	switch val := v.(type) {
	case []byte:
		data = val
	default:
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			return
		}
	}

	d.wsMu.RLock()
	relay := d.wsRelay
	d.wsMu.RUnlock()
	if relay != nil && relay.Connected() {
		relay.SendRaw(data)
	}
}

// startTSRelay background manager for TurnServer WebSocket connection (direct connect + auto-reconnect)
// exit loop when stopCh is closed, no more reconnection
func (d *Daemon) startTSRelay() {
	go func() {
		for {
			// check stop signal
			select {
			case <-d.stopCh:
				d.notifyState("stopped")
				return
			default:
			}

			// 1. call Worker API to get optimal TS
			d.notifyState("connecting")
			logPrintf(d.logger, "[ts]", "requesting Worker to assign TurnServer...")
			best, err := d.signaling.GetTurnServer()
			if err != nil {
				logErrorf(d.logger, "[ts]", "fetch failed: %v, retrying in 10s", err)
				if d.sleepOrStop(10 * time.Second) {
					d.notifyState("stopped")
					return
				}
				continue
			}
			if best == nil {
				logPrintf(d.logger, "[ts]", "no available TurnServer, retrying in 10s")
				if d.sleepOrStop(10 * time.Second) {
					d.notifyState("stopped")
					return
				}
				continue
			}

			// 2. connecting directly to TS
			relay := NewWSTurnRelay(d.accessKey, d.cfg, d.logger)
			relay.OnMessage = func(msg *Message) {
				d.handleMessage(msg, "TS")
			}
			relay.OnBinaryMessage = func(data []byte) {
				d.handleBinary(data)
			}
			relay.OnP2PSignal = func(raw map[string]any) {
				d.handleP2PSignal(raw)
			}

			logPrintf(d.logger, "[ts]", "connecting directly to TS: %s", best.WSURL())
			if err := relay.Connect(best.WSURL()); err != nil {
				logErrorf(d.logger, "[ts]", "connect/login failed: %v, retrying in 10s", err)
				if d.sleepOrStop(10 * time.Second) {
					d.notifyState("stopped")
					return
				}
				continue
			}

			// replace old connection
			d.wsMu.Lock()
			if d.wsRelay != nil {
				d.wsRelay.Close()
			}
			d.wsRelay = relay
			d.wsMu.Unlock()

			d.notifyState("connected")
			logPrintf(d.logger, "[ts]", "WebSocket connected, waiting...")

			// waiting for disconnect or stop
			select {
			case <-relay.Done():
				// connection disconnected
			case <-d.stopCh:
				relay.Close()
				d.notifyState("stopped")
				return
			}

			logPrintf(d.logger, "[ts]", "connection lost, reconnecting in 5s")

			// TS disconnected, cancel all in-progress file transfers
			if d.fileTransfer != nil {
				d.fileTransfer.CancelAll()
			}
			if d.proxyManager != nil {
				d.proxyManager.CloseAll()
			}

			// clear subscriptions, stop PTY output
			d.subMu.Lock()
			d.subscribed = make(map[string]bool)
			d.subMu.Unlock()

			d.wsMu.Lock()
			if d.wsRelay == relay {
				d.wsRelay = nil
			}
			d.wsMu.Unlock()

			if d.sleepOrStop(5 * time.Second) {
				d.notifyState("stopped")
				return
			}
		}
	}()
}

// sleepOrStop wait for duration, return true immediately if stopCh is closed
func (d *Daemon) sleepOrStop(duration time.Duration) bool {
	select {
	case <-d.stopCh:
		return true
	case <-time.After(duration):
		return false
	}
}

// notifyState notify state change (no-op when OnStateChange is nil, CLI version unaffected)
func (d *Daemon) notifyState(state string) {
	d.connState.Store(state)
	if d.OnStateChange != nil {
		d.OnStateChange(state)
	}
}

// GetState returns current connection state: "stopped", "connecting", "connected"
// returns "" before StartRemote is called
func (d *Daemon) GetState() string {
	if v := d.connState.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// Stop stop daemon connection loop (does not disconnect PTY), can be called by GUI
func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		close(d.stopCh)
		// also close current TS connection to speed up exit
		d.wsMu.RLock()
		relay := d.wsRelay
		d.wsMu.RUnlock()
		if relay != nil {
			relay.Close()
		}
		d.CloseRTC()
		if d.fileTransfer != nil {
			d.fileTransfer.CancelAll()
		}
		if d.proxyManager != nil {
			d.proxyManager.CloseAll()
		}
	})
}

// CloseRTC close current WebRTC connection (does not destroy PTY and TS connections)
func (d *Daemon) CloseRTC() {
	d.rtcMu.Lock()
	defer d.rtcMu.Unlock()
	if d.rtc != nil {
		d.rtc.Close()
		d.rtc = nil
	}
}

// Destroy destroy all resources (called on exit)
func (d *Daemon) Destroy() {
	// close file transfer
	if d.fileTransfer != nil {
		d.fileTransfer.CancelAll()
	}

	// close proxy tunnel
	if d.proxyManager != nil {
		d.proxyManager.CloseAll()
	}

	// close IPC server
	if d.ipcServer != nil {
		d.ipcServer.Close()
	}

	// close local WS service
	if d.localServer != nil {
		d.localServer.Close()
	}

	d.rtcMu.Lock()
	if d.rtc != nil {
		d.rtc.Close()
	}
	d.rtcMu.Unlock()

	d.wsMu.Lock()
	if d.wsRelay != nil {
		d.wsRelay.Close()
	}
	d.wsMu.Unlock()

	if d.ptyMgr != nil {
		d.ptyMgr.DestroyAll()
	}
	logPrintf(d.logger, "[daemon]", "destroyed")
}

// handleMessage handle messages from app (shared by WebRTC and WS)
func (d *Daemon) handleMessage(msg *Message, source string) {
	d.channelMu.RLock()
	mode := d.channelMode
	d.channelMu.RUnlock()
	logDebugf(d.logger, "[CHAN]", "← RECV via=%s type=%s currentMode=%s", source, msg.Type, mode)
	switch msg.Type {
	case TypeKicked:
		if msg.Reason == "daemon_replaced" {
			logPrintf(d.logger, "[daemon]", "kicked: new daemon with same accesskey connected, exiting")
			fmt.Println("kicked: new daemon with same accesskey connected, exiting")
			os.Exit(0)
		}
		logPrintf(d.logger, "[daemon]", "received kicked notification, cleaning all connections")
		atomic.StoreInt32(&d.kicked, 1)
		if d.fileTransfer != nil {
			d.fileTransfer.CancelAll()
		}
		if d.proxyManager != nil {
			d.proxyManager.CloseAll()
		}
		d.CloseRTC()
		d.rtcMu.Lock()
		d.pendingICE = nil
		d.pendingICEMode = false
		d.rtcMu.Unlock()
		d.channelMu.Lock()
		d.channelMode = ""
		d.channelMu.Unlock()
		d.subMu.Lock()
		d.subscribed = make(map[string]bool)
		d.subMu.Unlock()
	case TypePeerOffline:
		logPrintf(d.logger, "[daemon]", "received peer_offline (via handleMessage), current channel=%s", mode)
		d.handlePeerOffline()
	case TypeCreateSession:
		atomic.StoreInt32(&d.kicked, 0) // new frontend message, clearing kicked
		d.handleCreateSession(msg)
	case TypeDestroySession:
		d.ptyMgr.Destroy(msg.SessionID)
		d.subMu.Lock()
		delete(d.subscribed, msg.SessionID)
		d.subMu.Unlock()
		logPrintf(d.logger, "[daemon]", "session %s destroyed", msg.SessionID)
	case TypeInput:
		d.ptyMgr.Write(msg.SessionID, []byte(msg.Data))
	case TypeResize:
		if msg.Cols > 0 && msg.Rows > 0 {
			d.ptyMgr.Resize(msg.SessionID, msg.Cols, msg.Rows)
		}
	case TypeSessionList:
		atomic.StoreInt32(&d.kicked, 0) // new frontend message, clearing kicked
		d.handleSessionList()
	case TypeRequestHistory:
		d.handleHistory(msg)
	case TypePing:
		d.sendJSON(&Message{Type: TypePong})
	case TypeChannelSelect:
		d.handleChannelSelect(msg)
	case TypeFileSendCancel:
		d.handleFileSendCancel(msg)
	case TypeFileRequest:
		d.handleFileRequest(msg)
	case TypeFileListRequest:
		if d.fileTransfer != nil {
			d.fileTransfer.sendFileList()
		}
	case TypeFileDelete:
		d.handleFileDelete(msg)
	case TypeDirListRequest:
		if d.fileTransfer != nil {
			d.fileTransfer.HandleDirListRequest(msg.Data)
		}
	case TypeReqPending:
		if d.fileTransfer != nil {
			if err := d.fileTransfer.HandleReqPending(msg.Data); err != nil {
				d.sendJSON(&Message{
					Type:  TypeError,
					Error: err.Error(),
				})
			}
		}
	case TypeProxyConnect:
		if d.proxyManager != nil {
			d.proxyManager.HandleConnect(msg)
		}
	case TypeProxyClose:
		if d.proxyManager != nil {
			d.proxyManager.HandleClose(msg)
		}
	default:
		logPrintf(d.logger, "[daemon]", "unknown message type: %s", msg.Type)
	}
}

// handleBinary handle binary frames from frontend (routed by opcode)
func (d *Daemon) handleBinary(data []byte) {
	if len(data) == 0 {
		return
	}
	opcode := data[0]
	switch opcode {
	case OpcodeFileTransfer:
		logPrintf(d.logger, "[daemon]", "received frontend file transfer frame (%d bytes), not yet handled", len(data))
	case OpcodeProxyData:
		if len(data) < 5 {
			return
		}
		connID := binary.BigEndian.Uint32(data[1:5])
		payload := data[5:]
		if d.proxyManager != nil {
			d.proxyManager.HandleData(connID, payload)
		}
	default:
		logPrintf(d.logger, "[daemon]", "received unknown binary frame opcode=0x%02X, %d bytes", opcode, len(data))
	}
}

// handleCreateSession handle create session request
func (d *Daemon) handleCreateSession(msg *Message) {
	s, err := d.ptyMgr.Create(msg.SessionID, msg.Shell, d.cfg.DefaultCols, d.cfg.DefaultRows)
	if err != nil {
		d.sendJSON(&Message{
			Type:      TypeError,
			SessionID: msg.SessionID,
			Error:     err.Error(),
		})
		return
	}

	// session created by frontend, auto-subscribed
	d.subMu.Lock()
	d.subscribed[msg.SessionID] = true
	d.subMu.Unlock()

	logPrintf(d.logger, "[daemon]", "session %s created (pid=%d)", s.ID, s.PID)
	d.sendJSON(&Message{
		Type:      TypeSessionCreated,
		SessionID: s.ID,
		PID:       s.PID,
		Name:      s.Name,
	})
}

// handleSessionList return current session list with system info
func (d *Daemon) handleSessionList() {
	sessions := d.ptyMgr.ListSessions()
	logDebugf(d.logger, "[P2P-DEBUG-DA]", "handleSessionList: %d sessions", len(sessions))
	d.sendJSON(&Message{
		Type:         TypeSessionInfo,
		SessionInfos: sessions,
		SystemInfo:   getSystemInfo(),
	})
}

// getSystemInfo collect OS info for AI command translation
func getSystemInfo() string {
	if runtime.GOOS == "windows" {
		out, err := getWindowsVersion()
		if err == nil {
			return "Windows " + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Microsoft "))
		}
		return "Windows"
	}
	out, err := exec.Command("uname", "-sr").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return runtime.GOOS
}

// handleHistory return session history, re-serialize to client size, send in chunks
func (d *Daemon) handleHistory(msg *Message) {
	var data []byte
	var seq uint64

	if msg.Cols > 0 && msg.Rows > 0 {
		data, seq = d.ptyMgr.GetHistoryAt(msg.SessionID, msg.Cols, msg.Rows)
	} else {
		data, seq = d.ptyMgr.GetHistory(msg.SessionID)
	}

	if len(data) <= 4096 {
		d.sendJSON(&Message{
			Type:      TypeHistoryData,
			SessionID: msg.SessionID,
			Data:      string(data),
			Seq:       seq,
		})
	} else {
		d.sendHistoryChunks(msg.SessionID, data, seq)
	}

	// history sent, subscribing to real-time output
	d.subMu.Lock()
	d.subscribed[msg.SessionID] = true
	d.subMu.Unlock()

	// if this session has a local controller (GUI/CLI attach), close connection and kick it
	session := d.ptyMgr.Get(msg.SessionID)
	if session != nil && session.Controller != nil {
		traceLog("handleHistory: has Controller, type=%T", session.Controller)
		if closer, ok := session.Controller.(io.Closer); ok {
			traceLog("handleHistory: io.Closer assertion OK, calling Close()")
			closer.Close()
		} else {
			traceLog("handleHistory: io.Closer assertion FAILED")
		}
		session.Controller = nil
	} else {
		traceLog("handleHistory: no Controller (nil=%v)", session.Controller == nil)
	}
}

const historyChunkSize = 4096 // 4KB raw data per chunk

// sendHistoryChunks send history data in chunks
func (d *Daemon) sendHistoryChunks(sessionID string, data []byte, seq uint64) {
	total := (len(data) + historyChunkSize - 1) / historyChunkSize

	// ① send start first, indicating total chunks
	d.sendJSON(&Message{
		Type:        TypeHistoryStart,
		SessionID:   sessionID,
		Seq:         seq,
		TotalChunks: total,
	})

	// ② send chunk by chunk
	for i := 0; i < total; i++ {
		start := i * historyChunkSize
		end := start + historyChunkSize
		if end > len(data) {
			end = len(data)
		}

		d.channelMu.RLock()
		mode := d.channelMode
		d.channelMu.RUnlock()

		chunkMsg := &Message{
			Type:        TypeHistoryChunk,
			SessionID:   sessionID,
			Data:        string(data[start:end]),
			ChunkIndex:  i,
			TotalChunks: total,
		}

		if mode == "p2p" {
			d.rtcMu.RLock()
			rtc := d.rtc
			d.rtcMu.RUnlock()
			if rtc != nil {
				rtc.SendJSONWithBackpressure(chunkMsg, 64*1024)
			}
		} else {
			d.sendJSON(chunkMsg)
		}
	}

	// ③ send end completion signal
	d.sendJSON(&Message{
		Type:        TypeHistoryEnd,
		SessionID:   sessionID,
		TotalChunks: total,
	})
}

// handlePeerOffline handle browser offline notification
func (d *Daemon) handlePeerOffline() {
	d.channelMu.RLock()
	mode := d.channelMode
	d.channelMu.RUnlock()

	if mode == "p2p" {
		logPrintf(d.logger, "[daemon]", "P2P is active, ignoring TS layer peer_offline")
		return
	}

	logPrintf(d.logger, "[daemon]", "browser offline, releasing resources (mode=%s)", mode)
	if d.fileTransfer != nil {
		d.fileTransfer.CancelAll()
	}
	if d.proxyManager != nil {
		d.proxyManager.CloseAll()
	}
	d.subMu.Lock()
	d.subscribed = make(map[string]bool)
	d.subMu.Unlock()
	d.channelMu.Lock()
	d.channelMode = ""
	d.channelMu.Unlock()
}

// handleChannelSelect handle browser channel selection
func (d *Daemon) handleChannelSelect(msg *Message) {
	channel := msg.Data // "p2p" or "ts"
	if channel != "p2p" && channel != "ts" {
		logDebugf(d.logger, "[P2P-DEBUG-DA]", "invalid channel_select: %s", channel)
		return
	}

	d.channelMu.Lock()
	d.channelMode = channel
	d.channelMu.Unlock()

	logDebugf(d.logger, "[P2P-DEBUG-DA]", "channel switched to: %s", channel)

	d.sendJSON(&Message{
		Type: TypeChannelSelected,
		Data: channel,
	})
}

// handleFileSendCancel handle frontend request to cancel file send
func (d *Daemon) handleFileSendCancel(msg *Message) {
	if d.fileTransfer != nil && msg.FileID > 0 {
		logPrintf(d.logger, "[daemon]", "received file_send_cancel: file_id=%d", msg.FileID)
		d.fileTransfer.Cancel(msg.FileID)
	}
}

// handleFileRequest handle frontend request to transfer a file
func (d *Daemon) handleFileRequest(msg *Message) {
	if d.fileTransfer == nil {
		return
	}
	fileID := msg.FileID
	logDebugf(d.logger, "[CHAN]", "← RECV type=file_request file_id=%d", fileID)
	if err := d.fileTransfer.HandleRequest(fileID); err != nil {
		logErrorf(d.logger, "[daemon]", "file_request failed: %v", err)
		d.sendJSON(&Message{
			Type:  TypeError,
			Error: err.Error(),
		})
	}
}

// handleFileDelete handle frontend file deletion
func (d *Daemon) handleFileDelete(msg *Message) {
	if d.fileTransfer == nil {
		return
	}
	fileID := msg.FileID
	logDebugf(d.logger, "[CHAN]", "← RECV type=file_delete file_id=%d", fileID)
	d.fileTransfer.HandleDelete(fileID)
}

// replenishPool replenish session pool
func (d *Daemon) replenishPool() {
	current := len(d.ptyMgr.ListSessions())
	deficit := d.cfg.PoolSize - current
	if deficit <= 0 {
		return
	}
	for i := 0; i < deficit; i++ {
		id := d.ptyMgr.generateID()
		s, err := d.ptyMgr.Create(id, d.cfg.DefaultShell, d.cfg.DefaultCols, d.cfg.DefaultRows)
		if err != nil {
			logErrorf(d.logger, "[daemon]", "replenish session failed: %v", err)
			continue
		}
		logPrintf(d.logger, "[daemon]", "pool session %s (pid=%d)", s.ID, s.PID)
	}
}

// AttachLocalSession for local WS call: bind local controller to session, kick old client
func (d *Daemon) AttachLocalSession(sessionID string, ctrl io.Writer) error {
	attachLog("AttachLocalSession: enter sessionID=%s", sessionID)
	session := d.ptyMgr.Get(sessionID)
	if session == nil {
		attachLog("AttachLocalSession: session not found: %s", sessionID)
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// kick old controller (close its WS connection directly)
	if session.Controller != nil {
		attachLog("AttachLocalSession: has old Controller type=%T, closing...", session.Controller)
		t0 := time.Now()
		if closer, ok := session.Controller.(io.Closer); ok {
			closer.Close()
			attachLog("AttachLocalSession: old Controller closed, took=%dms", time.Since(t0).Milliseconds())
		} else {
			attachLog("AttachLocalSession: old Controller is NOT io.Closer (type=%T), skip close", session.Controller)
		}
	} else {
		attachLog("AttachLocalSession: no old Controller")
	}

	// set new controller
	session.Controller = ctrl
	attachLog("AttachLocalSession: new Controller set (type=%T)", ctrl)

	// unsubscribe app from this session, stop app from receiving output
	d.subMu.Lock()
	wasSubscribed := d.subscribed[sessionID]
	delete(d.subscribed, sessionID)
	d.subMu.Unlock()
	attachLog("AttachLocalSession: unsubscribed app (wasSubscribed=%v)", wasSubscribed)

	// send kicked notification via app channel
	attachLog("AttachLocalSession: sending kicked notification...")
	t0 := time.Now()
	d.sendJSON(&Message{
		Type:      TypeKicked,
		SessionID: sessionID,
		Data:      "this terminal has been taken over by a local connection",
	})
	attachLog("AttachLocalSession: kicked sent, took=%dms", time.Since(t0).Milliseconds())

	attachLog("AttachLocalSession: done OK")
	return nil
}

// DetachLocalSession disconnect local controller
func (d *Daemon) DetachLocalSession(sessionID string) {
	session := d.ptyMgr.Get(sessionID)
	if session != nil {
		session.Controller = nil
	}
}

// CreateSession create new session (for local WS call)
func (d *Daemon) CreateSession() (*Session, error) {
	id := d.ptyMgr.generateID()
	return d.ptyMgr.Create(id, d.cfg.DefaultShell, d.cfg.DefaultCols, d.cfg.DefaultRows)
}

// DestroySession destroy session
func (d *Daemon) DestroySession(sessionID string) {
	if s := d.ptyMgr.Get(sessionID); s != nil {
		if s.Controller != nil {
			s.Controller = nil
		}
		d.ptyMgr.Destroy(sessionID)
	}
}

// LocalTakeover local takeover: clear local controller, unsubscribe app, notify app kicked
func (d *Daemon) LocalTakeover() {
	// clear all session local controllers (close connections first, then clear references)
	for _, info := range d.ptyMgr.ListSessions() {
		if s := d.ptyMgr.Get(info.ID); s != nil {
			if s.Controller != nil {
				traceLog("LocalTakeover: closing Controller for session %s", info.ID)
				if closer, ok := s.Controller.(io.Closer); ok {
					closer.Close()
				}
				s.Controller = nil
			}
		}
	}

	// unsubscribe app from all sessions
	d.subMu.Lock()
	d.subscribed = make(map[string]bool)
	d.subMu.Unlock()

	// notify app kicked
	d.sendJSON(&Message{
		Type: TypeKicked,
		Data: "taken over locally",
	})
}

// sendOutput send terminal output (with write sequence number, used by frontend for dedup)
// send to local controller (local attach) first, then to subscribed app
func (d *Daemon) sendOutput(sessionID, data string, seq uint64) {
	// 1. send to local controller (if exists)
	session := d.ptyMgr.Get(sessionID)
	if session != nil && session.Controller != nil {
		traceHex("DAEMON PTY_READ>>", []byte(data))
		n, err := session.Controller.Write([]byte(data))
		if err != nil {
			attachLog("sendOutput: Controller.Write error: %v (wrote %d/%d bytes, sessionID=%s)", err, n, len(data), sessionID)
		}
	}

	// 2. send to subscribed app
	d.subMu.RLock()
	sub := d.subscribed[sessionID]
	d.subMu.RUnlock()
	if !sub {
		return
	}
	d.sendJSON(&Message{
		Type:      TypeOutput,
		SessionID: sessionID,
		Data:      data,
		Seq:       seq,
	})
}

// sendJSON send via current selected channel (single-channel mode, no auto-switch)
func (d *Daemon) sendJSON(msg *Message) {
	d.channelMu.RLock()
	mode := d.channelMode
	d.channelMu.RUnlock()

	ch := mode
	if ch == "" {
		ch = "ts(default)"
	}

	switch mode {
	case "p2p":
		d.rtcMu.RLock()
		rtc := d.rtc
		rtcOK := rtc != nil && rtc.Connected()
		d.rtcMu.RUnlock()
		if rtcOK {
			logDebugf(d.logger, "[CHAN]", "→ SEND via=%s type=%s session=%s", ch, msg.Type, msg.SessionID)
			rtc.SendJSON(msg)
			return
		}
		logDebugf(d.logger, "[CHAN]", "→ SEND via=%s type=%s DROPPED (rtc not connected)", ch, msg.Type)
		return

	case "ts":
		d.wsMu.RLock()
		relay := d.wsRelay
		d.wsMu.RUnlock()
		if relay != nil && relay.Connected() {
			logDebugf(d.logger, "[CHAN]", "→ SEND via=%s type=%s session=%s", ch, msg.Type, msg.SessionID)
			relay.SendJSON(msg)
			return
		}
		logPrintf(d.logger, "[daemon]", "TS channel is down, cannot send %s", msg.Type)

	default:
		d.wsMu.RLock()
		relay := d.wsRelay
		d.wsMu.RUnlock()
		if relay != nil && relay.Connected() {
			logDebugf(d.logger, "[CHAN]", "→ SEND via=%s type=%s session=%s", ch, msg.Type, msg.SessionID)
			relay.SendJSON(msg)
		}
	}
}

// sendBytes send binary data via current selected channel
func (d *Daemon) sendBytes(data []byte) {
	if len(data) > 32*1024 {
		logPrintf(d.logger, "[daemon]", "WARNING: sendBytes frame too large: %d bytes", len(data))
	}
	d.channelMu.RLock()
	mode := d.channelMode
	d.channelMu.RUnlock()

	switch mode {
	case "p2p":
		d.rtcMu.RLock()
		rtc := d.rtc
		rtcOK := rtc != nil && rtc.Connected()
		d.rtcMu.RUnlock()
		if rtcOK {
			rtc.SendBytes(data)
			return
		}
		return

	case "ts":
		d.wsMu.RLock()
		relay := d.wsRelay
		d.wsMu.RUnlock()
		if relay != nil && relay.Connected() {
			relay.SendBinary(data)
			return
		}
		logPrintf(d.logger, "[daemon]", "TS channel is down, cannot send binary data")

	default:
		d.wsMu.RLock()
		relay := d.wsRelay
		d.wsMu.RUnlock()
		if relay != nil && relay.Connected() {
			relay.SendBinary(data)
		}
	}
}

// sendBytesWithBackpressure send binary data via current selected channel (with backpressure)
func (d *Daemon) sendBytesWithBackpressure(data []byte, threshold uint64) {
	d.channelMu.RLock()
	mode := d.channelMode
	d.channelMu.RUnlock()

	switch mode {
	case "p2p":
		d.rtcMu.RLock()
		rtc := d.rtc
		rtcOK := rtc != nil && rtc.Connected()
		d.rtcMu.RUnlock()
		if rtcOK {
			rtc.SendBytesWithBackpressure(data, threshold)
			return
		}
		return

	default:
		d.wsMu.RLock()
		relay := d.wsRelay
		d.wsMu.RUnlock()
		if relay != nil && relay.Connected() {
			relay.SendBinaryBlocking(data)
		}
	}
}

// sendBytesCancelable blocking send with cancel (for file transfer, returns immediately when cancel is closed)
func (d *Daemon) sendBytesCancelable(data []byte, threshold uint64, cancel <-chan struct{}) bool {
	if len(data) > 32*1024 {
		logPrintf(d.logger, "[daemon]", "WARNING: sendBytesCancelable frame too large: %d bytes", len(data))
	}
	d.channelMu.RLock()
	mode := d.channelMode
	d.channelMu.RUnlock()

	switch mode {
	case "p2p":
		d.rtcMu.RLock()
		rtc := d.rtc
		rtcOK := rtc != nil && rtc.Connected()
		d.rtcMu.RUnlock()
		if rtcOK {
			for {
				select {
				case <-cancel:
					return false
				default:
				}
				rtc.mu.Lock()
				ba := rtc.dc.BufferedAmount()
				rtc.mu.Unlock()
				if ba < threshold {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			rtc.SendBytes(data)
			return true
		}
		return false

	default:
		d.wsMu.RLock()
		relay := d.wsRelay
		d.wsMu.RUnlock()
		if relay != nil && relay.Connected() {
			return relay.SendBinaryCancelable(data, cancel)
		}
		return false
	}
}

// hasActiveChannel check if there is an active client channel (P2P or TS)
func (d *Daemon) hasActiveChannel() bool {
	d.rtcMu.RLock()
	rtc := d.rtc
	rtcOK := rtc != nil && rtc.Connected()
	d.rtcMu.RUnlock()
	if rtcOK {
		return true
	}

	d.wsMu.RLock()
	relay := d.wsRelay
	wsOK := relay != nil && relay.Connected()
	d.wsMu.RUnlock()
	return wsOK
}

// startIPCServer start HTTP localhost IPC server (port range 56881-56981)
func (d *Daemon) startIPCServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/send", d.fileTransfer.handleIPCUpload)

	const portMin = 56881
	const portMax = 56981

	for port := portMin; port <= portMax; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		srv := &http.Server{Addr: addr, Handler: mux}

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			continue // port in use, try next
		}

		d.ipcServer = srv
		d.cfg.IPCHTTPPort = port // record actual bound port

		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				logErrorf(d.logger, "[daemon]", "IPC HTTP error: %v", err)
			}
		}()

		logPrintf(d.logger, "[daemon]", "IPC HTTP listening on %s", addr)
		return
	}

	logPrintf(d.logger, "[daemon]", "IPC HTTP: all ports %d-%d in use, skipping", portMin, portMax)
}

