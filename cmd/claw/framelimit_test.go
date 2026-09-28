package main

import "testing"

// Frame limit per channel. The P2P limit comes from pion/sctp's
// defaultMaxMessageSize = 65536; anything larger fails the send with
// ErrOutboundPacketTooLarge.
func TestFrameLimitForChannel(t *testing.T) {
	cases := []struct {
		name string
		mode string
		want int
	}{
		{"ts explicitly selected", "ts", MaxFrameSize},
		{"unselected defaults to TS", "", MaxFrameSize},
		{"p2p", "p2p", P2PMaxMessageSize},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &Daemon{}
			d.channelMode = c.mode
			if got := d.frameLimitForChannel(); got != c.want {
				t.Fatalf("frameLimitForChannel(%q) = %d, want %d", c.mode, got, c.want)
			}
			if got := d.channelIsP2P(); got != (c.mode == "p2p") {
				t.Fatalf("channelIsP2P(%q) = %v", c.mode, got)
			}
		})
	}
}

// Invariant: the P2P limit must stay strictly below pion's 65536, leaving room
// for the file header and the E2E envelope.
func TestP2PLimitFitsPion(t *testing.T) {
	const pionMax = 65536
	if P2PMaxMessageSize >= pionMax {
		t.Fatalf("P2PMaxMessageSize = %d must be < %d", P2PMaxMessageSize, pionMax)
	}
	// The 9B file header + encryption envelope must still fit.
	if P2PMaxMessageSize-9-SecureBinaryOverhead <= 0 {
		t.Fatalf("no chunk space left in the P2P limit after headers")
	}
	// The TS limit must be accepted by the relay (MAX_FRAME_BYTES).
	if MaxFrameSize <= P2PMaxMessageSize {
		t.Fatalf("MaxFrameSize = %d should be significantly larger than P2PMaxMessageSize = %d", MaxFrameSize, P2PMaxMessageSize)
	}
}

// File chunks must fit within the active channel limit (9B header + E2E
// envelope included).
func TestFileChunkFitsActiveChannel(t *testing.T) {
	for _, mode := range []string{"", "ts", "p2p"} {
		d := &Daemon{}
		d.channelMode = mode

		for _, secure := range []bool{false, true} {
			limit := d.frameLimitForChannel()
			overhead := 9
			if secure {
				overhead += SecureBinaryOverhead
			}
			chunk := limit - overhead
			if chunk <= 0 {
				t.Fatalf("mode=%q secure=%v no usable chunk space", mode, secure)
			}
			if chunk+overhead > limit {
				t.Fatalf("mode=%q secure=%v chunk+headers = %d exceeds limit %d", mode, secure, chunk+overhead, limit)
			}
		}
	}
}
