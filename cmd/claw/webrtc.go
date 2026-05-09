package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
)

// WebRTCConn manage WebRTC connection (using pion/webrtc, full TURN support)
type WebRTCConn struct {
	accessKey string
	cfg       *Config
	logger    Logger

	pc          *webrtc.PeerConnection
	dc          *webrtc.DataChannel
	pendingMsgs []string // buffered messages before DataChannel opens
	mu          sync.Mutex
	connected   bool

	// connection state notification
	connectCh    chan error // send nil on success, error on failure
	disconnectCh chan error // send error when disconnected after connection established

	// callback
	OnMessage          func(msg *Message)
	OnBinaryMessage    func(data []byte)
	OnICECandidateFunc func(candidate, sdpMid string, sdpMLineIndex int)

	// diagnostics: collected local candidates
	localCands []string
}

// NewWebRTCConn create WebRTC connection manager
func NewWebRTCConn(accessKey string, _ *SignalingClient, cfg *Config, logger Logger) *WebRTCConn {
	return &WebRTCConn{
		accessKey:    accessKey,
		cfg:          cfg,
		logger:       logger,
		connectCh:    make(chan error, 1),
		disconnectCh: make(chan error, 1),
	}
}

// Connected return whether DataChannel is open
func (w *WebRTCConn) Connected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.connected
}

// SendJSON send JSON message via DataChannel
func (w *WebRTCConn) SendJSON(msg *Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		logPrintf(w.logger, "[webrtc]", "serialize message failed: %v", err)
		return
	}
	text := string(data)

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.dc != nil && w.connected {
		w.dc.SendText(text)
	} else {
		w.pendingMsgs = append(w.pendingMsgs, text)
	}
}

// SendJSONWithBackpressure send with backpressure, for large data chunking scenarios
func (w *WebRTCConn) SendJSONWithBackpressure(msg *Message, threshold uint64) {
	data, err := json.Marshal(msg)
	if err != nil {
		logPrintf(w.logger, "[webrtc]", "serialize message failed: %v", err)
		return
	}
	text := string(data)

	for {
		w.mu.Lock()
		if w.dc == nil || !w.connected {
			w.pendingMsgs = append(w.pendingMsgs, text)
			w.mu.Unlock()
			return
		}
		ba := w.dc.BufferedAmount()
		w.mu.Unlock()

		if ba < threshold {
			break
		}
		logDebugf(w.logger, "[webrtc]", "backpressure wait: bufferedAmount=%d > threshold=%d", ba, threshold)
		time.Sleep(5 * time.Millisecond)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dc != nil && w.connected {
		w.dc.SendText(text)
	}
}

// SendBytes send binary data via DataChannel
func (w *WebRTCConn) SendBytes(data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dc != nil && w.connected {
		w.dc.Send(data)
	}
}

// SendBytesWithBackpressure binary send with backpressure
func (w *WebRTCConn) SendBytesWithBackpressure(data []byte, threshold uint64) {
	for {
		w.mu.Lock()
		if w.dc == nil || !w.connected {
			w.mu.Unlock()
			return
		}
		ba := w.dc.BufferedAmount()
		w.mu.Unlock()

		if ba < threshold {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dc != nil && w.connected {
		w.dc.Send(data)
	}
}

// Close close connection
func (w *WebRTCConn) Close() {
	if w.pc != nil {
		w.pc.Close()
	}
}

// Answer respond to SDP offer as answerer, return SDP answer
func (w *WebRTCConn) Answer(sdpOffer string, iceServers []webrtc.ICEServer) (string, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return "", fmt.Errorf("create PeerConnection failed: %w", err)
	}
	w.pc = pc

	// ICE candidate callback
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		cj := candidate.ToJSON()
		sdpMid := ""
		if cj.SDPMid != nil {
			sdpMid = *cj.SDPMid
		}
		idx := 0
		if cj.SDPMLineIndex != nil {
			idx = int(*cj.SDPMLineIndex)
		}
		logDebugf(w.logger, "[P2P-DEBUG-DA]", "local ICE (answerer): typ=%s %s:%d", candidate.Typ.String(), candidate.Address, candidate.Port)

		if w.OnICECandidateFunc != nil {
			w.OnICECandidateFunc(cj.Candidate, sdpMid, idx)
		}
	})

	// connection state callback
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		logDebugf(w.logger, "[P2P-DEBUG-DA]", "ICE state (answerer): %s", state.String())
		switch state {
		case webrtc.ICEConnectionStateConnected:
			select {
			case w.connectCh <- nil:
			default:
			}
		case webrtc.ICEConnectionStateFailed:
			w.logICEDiagnostics()
			select {
			case w.connectCh <- fmt.Errorf("ICE %s", state.String()):
			default:
			}
			select {
			case w.disconnectCh <- fmt.Errorf("ICE %s", state.String()):
			default:
			}
		case webrtc.ICEConnectionStateDisconnected:
			select {
			case w.disconnectCh <- fmt.Errorf("ICE %s", state.String()):
			default:
			}
		}
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logDebugf(w.logger, "[P2P-DEBUG-DA]", "connState (answerer): %s", state.String())
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			select {
			case w.connectCh <- fmt.Errorf("connection %s", state.String()):
			default:
			}
			select {
			case w.disconnectCh <- fmt.Errorf("connection %s", state.String()):
			default:
			}
		}
	})

	// receive DataChannel (answerer does not create, created by offerer)
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		logPrintf(w.logger, "[webrtc]", "received DataChannel: %s", dc.Label())
		w.dc = dc

		dc.OnOpen(func() {
			logPrintf(w.logger, "[webrtc]", "DataChannel opened")
			w.mu.Lock()
			w.connected = true
			pending := w.pendingMsgs
			w.pendingMsgs = nil
			w.mu.Unlock()
			for _, msg := range pending {
				dc.SendText(msg)
			}
		})

		dc.OnClose(func() {
			logPrintf(w.logger, "[webrtc]", "DataChannel closed")
			w.mu.Lock()
			w.connected = false
			w.mu.Unlock()
			select {
			case w.disconnectCh <- fmt.Errorf("DataChannel closed"):
			default:
			}
		})

		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if msg.IsString {
				var m Message
				if err := json.Unmarshal(msg.Data, &m); err != nil {
					return
				}
				if w.OnMessage != nil {
					w.OnMessage(&m)
				}
			} else {
				if w.OnBinaryMessage != nil {
					w.OnBinaryMessage(msg.Data)
				}
			}
		})
	})

	// set remote description (offer)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}); err != nil {
		return "", fmt.Errorf("set remote description failed: %w", err)
	}

	// create answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer failed: %w", err)
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local description failed: %w", err)
	}

	logPrintf(w.logger, "[webrtc]", "SDP answer generated (%d bytes), ICE candidates sent via TS WebSocket", len(pc.LocalDescription().SDP))
	return pc.LocalDescription().SDP, nil
}

// AddRemoteICE add remote ICE candidate
func (w *WebRTCConn) AddRemoteICE(candidate, sdpMid string, sdpMLineIndex int) error {
	if w.pc == nil {
		return fmt.Errorf("PeerConnection not initialized")
	}
	idx := uint16(sdpMLineIndex)
	return w.pc.AddICECandidate(webrtc.ICECandidateInit{
		Candidate:     candidate,
		SDPMid:        &sdpMid,
		SDPMLineIndex: &idx,
	})
}

// WaitConnected block waiting for connection or timeout
func (w *WebRTCConn) WaitConnected(timeout time.Duration) error {
	select {
	case err := <-w.connectCh:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("connection timeout (%v)", timeout)
	}
}

// WaitDisconnected block waiting for disconnection
func (w *WebRTCConn) WaitDisconnected() error {
	return <-w.disconnectCh
}

// logICEDiagnostics output diagnostic info on ICE failure
func (w *WebRTCConn) logICEDiagnostics() {
	logPrintf(w.logger, "[webrtc]", "===== ICE diagnostics =====")
	logPrintf(w.logger, "[webrtc]", "local candidate count: %d", len(w.localCands))
	for _, c := range w.localCands {
		logPrintf(w.logger, "[webrtc]", "  local: %s", c)
	}
	logPrintf(w.logger, "[webrtc]", "===== diagnostics end =====")
}
