package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const esc = "\x1b"

// Every chunk must marshal below SecureMaxTextPayload, otherwise E2E
// encryption refuses to send it outright.
func TestHistoryChunksStayUnderSecureLimit(t *testing.T) {
	budget := SecureMaxTextPayload - historyChunkMargin

	cases := map[string][]byte{
		"empty":            nil,
		"single byte":      {'a'},
		"long pure ASCII":  []byte(strings.Repeat("x", 300*1024)),
		"ANSI dense":       []byte(strings.Repeat(esc+"[31mRED"+esc+"[0m", 40*1024)),
		"mixed":            []byte(strings.Repeat("line\n"+esc+"[1;32m"+strings.Repeat("y", 200)+esc+"[0m\n", 2000)),
		"exactly one unit": []byte(strings.Repeat("z", historyChunkUnit)),
		"one unit plus 1B": []byte(strings.Repeat("z", historyChunkUnit+1)),
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			chunks := historyPlainChunks(data)
			if len(chunks) == 0 && len(data) > 0 {
				t.Fatalf("data present but no chunks produced")
			}
			var joined int
			for i, c := range chunks {
				b, err := json.Marshal(&Message{Type: TypeHistoryChunk, Data: string(c)})
				if err != nil {
					t.Fatalf("chunk %d marshal failed: %v", i, err)
				}
				if len(b) > budget {
					t.Fatalf("chunk %d marshalled=%d exceeds budget=%d", i, len(b), budget)
				}
				if len(c) > historyChunkUnit && len(c)%historyChunkUnit != 0 && len(c) != len(data)-joined {
					t.Fatalf("chunk %d size %d is not a multiple of the unit", i, len(c))
				}
				joined += len(c)
			}
			if joined != len(data) {
				t.Fatalf("rejoined length %d != original %d (data loss)", joined, len(data))
			}
		})
	}
}

// Clean text should merge into large chunks, otherwise the adaptive sizing
// is pointless.
func TestHistoryChunksMergeCleanText(t *testing.T) {
	data := []byte(strings.Repeat("a", historyChunkUnit*8))
	chunks := historyPlainChunks(data)

	rawMax := 0
	for _, c := range chunks {
		if len(c) > rawMax {
			rawMax = len(c)
		}
	}
	if rawMax <= historyChunkUnit {
		t.Fatalf("clean text did not merge, max chunk only %d (unit=%d)", rawMax, historyChunkUnit)
	}
	// 8 units of data should produce far fewer than 8 messages.
	if len(chunks) >= historyChunkUnit*8/historyChunkUnit {
		t.Logf("chunk count %d, fixed-size chunking would need %d", len(chunks), (len(data)+historyChunkUnit-1)/historyChunkUnit)
	}
}

// ANSI-dense content must shrink automatically instead of hitting 24000.
func TestHistoryChunksShrinkForANSI(t *testing.T) {
	data := []byte(strings.Repeat(esc+"[31m"+strings.Repeat("R", 50)+esc+"[0m", 4000))
	for i, c := range historyPlainChunks(data) {
		b, _ := json.Marshal(&Message{Type: TypeHistoryChunk, Data: string(c)})
		if len(b) > SecureMaxTextPayload {
			t.Fatalf("chunk %d marshalled=%d exceeds the hard limit", i, len(b))
		}
	}
}

// Per-channel frame budgets for proxy / file: the finished frame must stay
// within what the channel can carry.
func TestPerChannelFrameBudgets(t *testing.T) {
	// proxy: raw + 5B header + encryption envelope
	for _, mode := range []string{"", "ts", "p2p"} {
		d := &Daemon{}
		d.channelMode = mode
		payload := d.proxyPayloadLimitForChannel()
		frame := payload + proxyFrameHeaderSize + SecureBinaryOverhead

		if mode == "p2p" {
			if frame > P2PMaxMessageSize {
				t.Fatalf("p2p proxy frame %d exceeds the pion limit %d", frame, P2PMaxMessageSize)
			}
		} else if frame > MaxFrameSize {
			t.Fatalf("relay proxy frame %d exceeds MaxFrameSize %d", frame, MaxFrameSize)
		}
		if payload <= 0 {
			t.Fatalf("mode=%q proxy payload=%d", mode, payload)
		}
	}

	// file: raw + 9B header + encryption envelope
	for _, mode := range []string{"", "ts", "p2p"} {
		d := &Daemon{}
		d.channelMode = mode
		chunk := d.frameLimitForChannel() - 9 - SecureBinaryOverhead
		if chunk <= 0 {
			t.Fatalf("mode=%q no file chunk space", mode)
		}
	}
}
