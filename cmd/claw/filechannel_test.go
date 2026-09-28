package main

// filechannel_test.go — unit tests for the dedicated P2P file DataChannel:
//   1. the E2E envelope's cleartext innerOpcode mirror (routing without decryption)
//   2. frameRoutesToFile routing decisions (including the 0x07 envelope inner layer)
//   3. binaryTargetLocked channel selection and the old-app fallback
//   4. SecureBinaryOverhead matches the actual envelope length

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
)

func TestWrapBinaryInnerOpcodeMirror(t *testing.T) {
	daemon, app := testChannels(t)

	for _, frame := range [][]byte{
		{OpcodeFileTransfer, 0, 0, 0, 1, 0, 0, 0, 0, 'a', 'b'},
		{OpcodeProxyData, 0, 0, 0, 1, 'x'},
	} {
		wire := daemon.WrapBinary(frame)
		if wire == nil {
			t.Fatal("WrapBinary returned nil with active channel")
		}
		if wire[0] != OpcodeSecureBinary {
			t.Fatalf("first byte: want 0x07, got %x", wire[0])
		}
		if wire[9] != frame[0] {
			t.Fatalf("innerOpcode mirror: want %x, got %x", frame[0], wire[9])
		}
		if len(wire) != len(frame)+SecureBinaryOverhead {
			t.Fatalf("envelope length: want len+%d, got %d (frame=%d)",
				SecureBinaryOverhead, len(wire), len(frame))
		}
		got, err := app.UnwrapBinary(wire)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, frame) {
			t.Fatalf("roundtrip mangled: %x", got)
		}
	}
}

func TestFrameRoutesToFile(t *testing.T) {
	wrapped := func(inner byte) []byte {
		daemon, _ := testChannels(t)
		return daemon.WrapBinary([]byte{inner, 1, 2, 3})
	}

	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"plaintext file frame", []byte{OpcodeFileTransfer, 0, 0, 0, 1}, true},
		{"plaintext proxy frame", []byte{OpcodeProxyData, 0, 0, 0, 1}, false},
		{"wrapped file frame", wrapped(OpcodeFileTransfer), true},
		{"wrapped proxy frame", wrapped(OpcodeProxyData), false},
		{"empty", nil, false},
		{"truncated envelope", []byte{OpcodeSecureBinary, 1, 2, 3}, false},
	}
	for _, c := range cases {
		if got := frameRoutesToFile(c.data); got != c.want {
			t.Errorf("%s: want %v, got %v", c.name, c.want, got)
		}
	}
}

func TestBinaryTargetRouting(t *testing.T) {
	termDc := new(webrtc.DataChannel)
	fileDc := new(webrtc.DataChannel)
	fileFrame := []byte{OpcodeFileTransfer, 0, 0, 0, 1}
	proxyFrame := []byte{OpcodeProxyData, 0, 0, 0, 1}

	// file channel ready: file frames go to fileDc, everything else to terminal
	w := &WebRTCConn{dc: termDc, fileDc: fileDc, termConnected: true, fileConnected: true}
	if dc, ok := w.binaryTargetLocked(fileFrame); !ok || dc != fileDc {
		t.Errorf("file frame should route to fileDc, got %v %v", dc, ok)
	}
	if dc, ok := w.binaryTargetLocked(proxyFrame); !ok || dc != termDc {
		t.Errorf("proxy frame should route to terminal, got %v %v", dc, ok)
	}

	// file channel exists but is not open (reset window): file frames must NOT
	// fall back to terminal (that is exactly the head-of-line blocking being
	// fixed here) — dropping is preferable
	w.fileConnected = false
	if _, ok := w.binaryTargetLocked(fileFrame); ok {
		t.Error("file frame must not fall back to terminal while fileDc reopens")
	}
	if dc, ok := w.binaryTargetLocked(proxyFrame); !ok || dc != termDc {
		t.Errorf("proxy frame should stay on terminal, got %v %v", dc, ok)
	}

	// old app: never created a file channel (fileDc == nil) → fall back to terminal for compatibility
	w.fileDc = nil
	if dc, ok := w.binaryTargetLocked(fileFrame); !ok || dc != termDc {
		t.Errorf("old-app fallback should route file frame to terminal, got %v %v", dc, ok)
	}

	// neither channel open
	w2 := &WebRTCConn{}
	if _, ok := w2.binaryTargetLocked(fileFrame); ok {
		t.Error("no open channel must report unavailable")
	}
	if _, ok := w2.binaryTargetLocked(proxyFrame); ok {
		t.Error("no open channel must report unavailable")
	}
}

func TestAwaitAckWindow(t *testing.T) {
	ftm := &FileTransferManager{}
	task := &FileSendTask{
		ackNotify: make(chan struct{}, 1),
		cancel:    make(chan struct{}),
	}

	// inside the window (chunkIdx < acked+window): pass immediately
	if r := ftm.awaitAckWindow(task, 0); r != ackWaitReady {
		t.Errorf("chunk 0 should pass immediately, got %v", r)
	}
	if r := ftm.awaitAckWindow(task, fileAckWindow-1); r != ackWaitReady {
		t.Errorf("chunk %d should pass inside window, got %v", fileAckWindow-1, r)
	}

	// window boundary: acked=0, chunkIdx=window → block for an ack; passes once the ack advances
	go func() {
		<-time.After(20 * time.Millisecond)
		task.acked.Store(1)
		select {
		case task.ackNotify <- struct{}{}:
		default:
		}
	}()
	if r := ftm.awaitAckWindow(task, fileAckWindow); r != ackWaitReady {
		t.Errorf("chunk %d should pass after ack, got %v", fileAckWindow, r)
	}

	// cancel ends the wait
	task2 := &FileSendTask{
		ackNotify: make(chan struct{}, 1),
		cancel:    make(chan struct{}),
	}
	go func() {
		<-time.After(20 * time.Millisecond)
		close(task2.cancel)
	}()
	if r := ftm.awaitAckWindow(task2, fileAckWindow); r != ackWaitCancelled {
		t.Errorf("cancelled wait should return ackWaitCancelled, got %v", r)
	}
}

func TestHandleAckUpdatesAndWakes(t *testing.T) {
	ftm := &FileTransferManager{active: map[uint32]*FileSendTask{}}
	task := &FileSendTask{
		ID:        7,
		ackNotify: make(chan struct{}, 1),
		cancel:    make(chan struct{}),
	}
	ftm.active[7] = task

	// unknown task: no-op, no panic
	ftm.HandleAck(999, 5)

	// regressed/duplicate acks are rejected (received only ever increases)
	ftm.HandleAck(7, 3)
	if task.acked.Load() != 3 {
		t.Fatalf("acked should be 3, got %d", task.acked.Load())
	}
	ftm.HandleAck(7, 2)
	if task.acked.Load() != 3 {
		t.Fatalf("regressed ack must be ignored, got %d", task.acked.Load())
	}

	// an advancing ack wakes a parked sender
	ftm.HandleAck(7, 5)
	select {
	case <-task.ackNotify:
	default:
		t.Fatal("advancing ack should wake a parked sender")
	}
	if task.acked.Load() != 5 {
		t.Fatalf("acked should be 5, got %d", task.acked.Load())
	}
}

// relay chunks tighten to FileRelayChunk; p2p keeps P2PMaxMessageSize.
func TestFileChunkLimitFor(t *testing.T) {
	for _, mode := range []string{"", "ts"} {
		d := &Daemon{}
		d.channelMode = mode
		if got := fileChunkLimitFor(d); got != FileRelayChunk {
			t.Errorf("mode=%q: want FileRelayChunk=%d, got %d", mode, FileRelayChunk, got)
		}
	}
	d := &Daemon{}
	d.channelMode = "p2p"
	if got := fileChunkLimitFor(d); got != P2PMaxMessageSize {
		t.Errorf("p2p: want P2PMaxMessageSize=%d, got %d", P2PMaxMessageSize, got)
	}
	// the in-flight budget (window × chunk) must stay far below the
	// whole-file backlog possible without flow control, and the window must
	// match the app's decrypt worker count
	if fileAckWindow != 4 {
		t.Errorf("fileAckWindow should match the app's 4 decrypt workers, got %d", fileAckWindow)
	}
	if fileAckWindow*FileRelayChunk != 4*1024*1024 {
		t.Errorf("relay in-flight budget changed: %d bytes", fileAckWindow*FileRelayChunk)
	}
}
