package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// push_pause: the app asks the daemon to stop pushing output (relay messages
// cost DO billing) while history keeps accumulating; request_history lifts
// the mute. These tests cover the state machine at the Daemon level using a
// recorder-backed batcher (no PTY / relay needed). nopLogger comes from
// secure_integration_test.go.

func newMuteTestDaemon(subscribed ...string) (*Daemon, *rec) {
	r := &rec{}
	d := &Daemon{logger: nopLogger{}}
	d.ptyMgr = &PTYManager{} // zero value: Get() on a nil sessions map is a safe miss
	// relayConnected() gate: a zero WSTurnRelay with connected=true passes it
	// without a real socket (SendJSON inside would no-op, but the batcher
	// recorder captures before any channel routing).
	d.wsRelay = &WSTurnRelay{connected: true}
	// sendOutput's forced-encryption gate: pretend the handshake finished.
	atomic.StoreInt32(&d.secCodeVerified, 1)
	d.subscribed = make(map[string]bool)
	d.muted = make(map[string]bool)
	for _, id := range subscribed {
		d.subscribed[id] = true
	}
	d.outBatcher = newOutputBatcher(outputBatcherDeps{
		now:         func() time.Time { return time.Unix(0, 0) },
		interactive: always(false),
		send:        r.send,
	})
	return d, r
}

// A muted session's output never reaches the batcher.
func TestPushPauseDropsOutput(t *testing.T) {
	d, r := newMuteTestDaemon("s1")

	d.handlePushPause(&Message{Type: TypePushPause, SessionID: "s1"})
	d.sendOutput("s1", "hello", 1)

	if n := len(r.snapshot()); n != 0 {
		t.Fatalf("muted session output must not be dispatched, got %d messages", n)
	}
	d.outBatcher.mu.Lock()
	bufs := len(d.outBatcher.bufs)
	d.outBatcher.mu.Unlock()
	if bufs != 0 {
		t.Fatalf("muted session output must not even enter the batcher, got %d bufs", bufs)
	}
}

// push_pause with an empty SessionID mutes every subscribed session.
func TestPushPauseAllExpandsToSubscribed(t *testing.T) {
	d, _ := newMuteTestDaemon("s1", "s2")

	d.handlePushPause(&Message{Type: TypePushPause})

	d.subMu.RLock()
	mutedS1 := d.muted["s1"]
	mutedS2 := d.muted["s2"]
	d.subMu.RUnlock()
	if !mutedS1 || !mutedS2 {
		t.Fatalf("pause-all must mute every subscribed session, got s1=%v s2=%v", mutedS1, mutedS2)
	}
}

// request_history (unmutePush) lifts the mute and output flows again.
func TestPushUnmuteResumesOutput(t *testing.T) {
	d, r := newMuteTestDaemon("s1")

	d.handlePushPause(&Message{Type: TypePushPause, SessionID: "s1"})
	d.sendOutput("s1", "dropped", 1)

	d.unmutePush("s1")
	d.sendOutput("s1", "resumed", 2)
	d.outBatcher.FlushAll()

	msgs := r.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("expected only the post-unmute output, got %d messages", len(msgs))
	}
	if msgs[0].Data != "resumed" || msgs[0].Seq != 2 {
		t.Fatalf("unexpected message %+v", msgs[0])
	}
}

// unmute is a no-op for sessions that were never muted (history requests
// arrive all the time during normal operation).
func TestPushUnmuteIdempotent(t *testing.T) {
	d, _ := newMuteTestDaemon("s1")
	d.unmutePush("s1") // must not panic or create state
	d.subMu.RLock()
	_, exists := d.muted["s1"]
	d.subMu.RUnlock()
	if exists {
		t.Fatal("unmute must not introduce state")
	}
}

// push_pause flushes the batcher before muting, so nothing buffered slips
// out after the mute takes effect.
func TestPushPauseFlushesBeforeMuting(t *testing.T) {
	d, r := newMuteTestDaemon("s1")

	d.sendOutput("s1", "buffered", 1)
	d.handlePushPause(&Message{Type: TypePushPause, SessionID: "s1"})

	msgs := r.snapshot()
	if len(msgs) != 1 || msgs[0].Data != "buffered" {
		t.Fatalf("pre-mute buffered output should be flushed exactly once, got %+v", msgs)
	}

	// anything after the mute is dropped
	d.sendOutput("s1", "after", 2)
	if n := len(r.snapshot()); n != 1 {
		t.Fatalf("post-mute output leaked: %d messages", n)
	}
}

// the app drives push_pause through the normal message path (plaintext,
// secure channel not established).
func TestHandleMessageRoutesPushPause(t *testing.T) {
	d, _ := newMuteTestDaemon("s1")

	d.handleMessage(&Message{Type: TypePushPause, SessionID: "s1"}, "test")

	d.subMu.RLock()
	muted := d.muted["s1"]
	d.subMu.RUnlock()
	if !muted {
		t.Fatal("handleMessage must route push_pause to the mute state")
	}
}

// concurrent Add/flush/mute from multiple goroutines must not race (run with
// -race).
func TestPushPauseConcurrent(t *testing.T) {
	d, _ := newMuteTestDaemon("s1", "s2")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				switch n % 3 {
				case 0:
					d.sendOutput("s1", "x", uint64(j+1))
				case 1:
					d.handlePushPause(&Message{Type: TypePushPause})
				case 2:
					d.unmutePush("s2")
				}
			}
		}(i)
	}
	wg.Wait()
}
