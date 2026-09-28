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

	pc *webrtc.PeerConnection

	// Two independent SCTP streams (see P2P_DATACHANNEL_FIX.md):
	//   - dc ("terminal"): text + control binary, ordered. The only stream
	//     whose loss means the connection is gone.
	//   - fileDc ("file"): bulk OpcodeFileTransfer chunks. Closing it issues
	//     an SCTP stream reset that discards queued-but-undelivered chunks,
	//     so a cancelled transfer never blocks the terminal stream
	//     (head-of-line blocking fix).
	dc     *webrtc.DataChannel
	fileDc *webrtc.DataChannel

	pendingMsgs   []string // buffered text while the terminal channel is not open yet
	mu            sync.Mutex
	termConnected bool
	fileConnected bool

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
func NewWebRTCConn(accessKey string, cfg *Config, logger Logger) *WebRTCConn {
	return &WebRTCConn{
		accessKey:    accessKey,
		cfg:          cfg,
		logger:       logger,
		connectCh:    make(chan error, 1),
		disconnectCh: make(chan error, 1),
	}
}

// Connected reports whether the terminal DataChannel is open. Only the
// terminal stream carries control traffic, so it defines "connected".
func (w *WebRTCConn) Connected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.termConnected
}

// FileChannelOpen reports whether the app's dedicated file DataChannel is up.
// False for older apps that only ever create "terminal" (senders then fall
// back to the terminal stream for bulk frames).
func (w *WebRTCConn) FileChannelOpen() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fileConnected
}

// frameRoutesToFile reports whether a binary frame belongs on the file
// DataChannel. Only OpcodeFileTransfer bulk frames are split out this round
// (proxy stays on terminal — its volume is small; see the fix plan). For the
// E2E envelope the inner opcode is mirrored in the clear at byte 9
// ([0x07][8B seq][1B innerOpcode][ct]) precisely so this needs no decryption.
func frameRoutesToFile(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if data[0] == OpcodeFileTransfer {
		return true
	}
	return data[0] == OpcodeSecureBinary && len(data) > 9 && data[9] == OpcodeFileTransfer
}

// binaryTargetLocked picks the outgoing DataChannel for a binary frame.
// File-routed frames go to the file stream when the app supports it; when the
// app opened a file channel that is currently (re)opening — e.g. right after a
// cancel-triggered reset — the frame is NOT silently diverted to the terminal
// stream, because a bulk backlog there is exactly the head-of-line blocking
// this split exists to prevent. Older apps that never create a file channel
// (fileDc == nil) keep the old single-stream behavior.
// Caller must hold w.mu.
func (w *WebRTCConn) binaryTargetLocked(data []byte) (*webrtc.DataChannel, bool) {
	if frameRoutesToFile(data) {
		if w.fileDc == nil {
			// older app: no file channel exists, fall back to terminal
		} else if w.fileConnected {
			return w.fileDc, true
		} else {
			return nil, false
		}
	}
	if w.dc != nil && w.termConnected {
		return w.dc, true
	}
	return nil, false
}

// SendJSON send JSON message via the terminal DataChannel
func (w *WebRTCConn) SendJSON(msg *Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	text := string(data)

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.dc != nil && w.termConnected {
		w.dc.SendText(text)
	} else {
		w.pendingMsgs = append(w.pendingMsgs, text)
	}
}

// SendJSONWithBackpressure send with backpressure, for large data chunking scenarios
func (w *WebRTCConn) SendJSONWithBackpressure(msg *Message, threshold uint64) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	text := string(data)

	for {
		w.mu.Lock()
		if w.dc == nil || !w.termConnected {
			w.pendingMsgs = append(w.pendingMsgs, text)
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
	if w.dc != nil && w.termConnected {
		w.dc.SendText(text)
	}
}

// SendBytes send binary data via the routed DataChannel (file frames → fileDc)
func (w *WebRTCConn) SendBytes(data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	dc, ok := w.binaryTargetLocked(data)
	if !ok {
		return
	}
	if err := dc.Send(data); err != nil {
		// The old code discarded the error, so pion failures like
		// ErrOutboundPacketTooLarge vanished silently.
		w.logger.Errorf("[P2P] dc.Send failed: %v", err)
		return
	}
}

// SendBytesWithBackpressure binary send with backpressure on the routed channel
func (w *WebRTCConn) SendBytesWithBackpressure(data []byte, threshold uint64) {
	for {
		w.mu.Lock()
		dc, ok := w.binaryTargetLocked(data)
		if !ok {
			w.mu.Unlock()
			return
		}
		ba := dc.BufferedAmount()
		w.mu.Unlock()

		if ba < threshold {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if dc, ok := w.binaryTargetLocked(data); ok {
		dc.Send(data)
	}
}

// SendBytesCancelable blocking send with cancel (file transfer; returns false
// when the cancel channel fired or the target channel is unavailable, e.g.
// mid-reset). Backpressure is measured on whichever stream the frame routes to.
func (w *WebRTCConn) SendBytesCancelable(data []byte, threshold uint64, cancel <-chan struct{}) bool {
	for {
		select {
		case <-cancel:
			return false
		default:
		}
		w.mu.Lock()
		dc, ok := w.binaryTargetLocked(data)
		if !ok {
			w.mu.Unlock()
			return false
		}
		ba := dc.BufferedAmount()
		w.mu.Unlock()
		if ba < threshold {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	dc, ok := w.binaryTargetLocked(data)
	if !ok {
		return false
	}
	if err := dc.Send(data); err != nil {
		w.logger.Errorf("[P2P] dc.Send failed: %v", err)
	}
	return true
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

		if w.OnICECandidateFunc != nil {
			w.OnICECandidateFunc(cj.Candidate, sdpMid, idx)
		}
	})

	// connection state callback
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		switch state {
		case webrtc.ICEConnectionStateConnected:
			select {
			case w.connectCh <- nil:
			default:
			}
		case webrtc.ICEConnectionStateFailed:
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

	// receive DataChannel (answerer does not create, created by offerer).
	// The app creates "terminal" + "file"; unknown labels are treated as
	// terminal so older apps that only create one channel keep working.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == dcLabelFile {
			w.mu.Lock()
			w.fileDc = dc
			w.mu.Unlock()
			w.registerFileChannel(dc)
			return
		}
		w.mu.Lock()
		w.dc = dc
		w.mu.Unlock()
		w.registerTerminalChannel(dc)
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

// registerTerminalChannel wires the OnOpen/OnClose/OnMessage handlers of the
// terminal stream: flushes text buffered before the channel opened, reports
// connection loss on close, and dispatches text/binary messages.
func (w *WebRTCConn) registerTerminalChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		w.mu.Lock()
		w.termConnected = true
		pending := w.pendingMsgs
		w.pendingMsgs = nil
		w.mu.Unlock()
		// Messages buffered while the channel was not open yet finally go out now.
		for _, msg := range pending {
			dc.SendText(msg)
		}
	})

	dc.OnClose(func() {
		w.mu.Lock()
		w.termConnected = false
		queued := len(w.pendingMsgs)
		w.mu.Unlock()
		// Messages still buffered at OnClose will never be sent — must be visible.
		if queued > 0 {
			w.logger.Warnf("[P2P] terminal channel closed with %d messages still buffered", queued)
		}
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
}

// registerFileChannel wires the file stream handlers. Closing this channel is
// a NORMAL flow (the app resets it to flush a cancelled transfer's backlog),
// so OnClose only flips the open flag — it must never signal disconnectCh.
func (w *WebRTCConn) registerFileChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		w.mu.Lock()
		w.fileConnected = true
		w.mu.Unlock()
	})

	dc.OnClose(func() {
		w.mu.Lock()
		w.fileConnected = false
		w.mu.Unlock()
		// A reset here is the app cancelling a transfer — a normal flow, not a
		// connection loss, so it is deliberately not logged.
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if msg.IsString {
			// The file channel should never carry text; fall back to terminal
			// message dispatch for robustness.
			var m Message
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				return
			}
			if w.OnMessage != nil {
				w.OnMessage(&m)
			}
			return
		}
		if w.OnBinaryMessage != nil {
			w.OnBinaryMessage(msg.Data)
		}
	})
}
