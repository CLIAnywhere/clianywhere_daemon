package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder captures dispatched messages in order.
type rec struct {
	mu   sync.Mutex
	msgs []*Message
}

func (r *rec) send(msg *Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *msg
	r.msgs = append(r.msgs, &cp)
}

func (r *rec) snapshot() []*Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Message, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// fakeClock is an injectable wall clock (tests never start the flusher
// goroutine; they call tick() directly with a controlled now).
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestBatcher(now func() time.Time, interactive func() bool) (*outputBatcher, *rec) {
	r := &rec{}
	b := newOutputBatcher(outputBatcherDeps{
		now:         now,
		interactive: interactive,
		send:        r.send,
	})
	return b, r
}

func always(b bool) func() bool { return func() bool { return b } }

// merges: N Adds then one FlushAll produce a single message whose n / Seq /
// Data describe exactly those writes.
func TestOutputBatcherMergesWrites(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b, r := newTestBatcher(clock.Now, always(false))

	b.Add("s1", []byte("hello "), 1)
	b.Add("s1", []byte("brave "), 2)
	b.Add("s1", []byte("world"), 3)
	b.FlushAll()

	msgs := r.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Type != TypeOutput || m.SessionID != "s1" {
		t.Fatalf("unexpected envelope: %+v", m)
	}
	if m.Outputs != 3 {
		t.Fatalf("Outputs = %d, want 3", m.Outputs)
	}
	if m.Seq != 3 {
		t.Fatalf("Seq = %d, want 3 (last write)", m.Seq)
	}
	if m.Data != "hello brave world" {
		t.Fatalf("Data = %q", m.Data)
	}
}

// interactive tier flushes at the 10ms deadline, not before.
func TestOutputBatcherInteractiveDeadline(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b, r := newTestBatcher(clock.Now, always(true))

	b.Add("s1", []byte("a"), 1)

	clock.Advance(9 * time.Millisecond)
	b.tick()
	if n := len(r.snapshot()); n != 0 {
		t.Fatalf("flushed %d messages before the interactive deadline", n)
	}

	clock.Advance(1 * time.Millisecond) // 10ms total
	b.tick()
	if n := len(r.snapshot()); n != 1 {
		t.Fatalf("expected flush at the 10ms interactive deadline, got %d", n)
	}
}

// idle tier flushes at the 300ms deadline, not before.
func TestOutputBatcherIdleDeadline(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b, r := newTestBatcher(clock.Now, always(false))

	b.Add("s1", []byte("a"), 1)

	clock.Advance(299 * time.Millisecond)
	b.tick()
	if n := len(r.snapshot()); n != 0 {
		t.Fatalf("flushed %d messages before the idle deadline", n)
	}

	clock.Advance(1 * time.Millisecond) // 300ms total
	b.tick()
	if n := len(r.snapshot()); n != 1 {
		t.Fatalf("expected flush at the 300ms idle deadline, got %d", n)
	}
}

// reaching outputRawCap flushes immediately regardless of the time window.
func TestOutputBatcherRawCapEarlyFlush(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b, r := newTestBatcher(clock.Now, always(false))

	big := strings.Repeat("x", outputRawCap)
	b.Add("s1", []byte(big), 1)

	// age 0 — the raw cap alone must trigger the flush (24KB raw may exceed
	// the marshalled budget, so it can legitimately arrive as 2 messages)
	b.tick()
	msgs := r.snapshot()
	var flushed strings.Builder
	for _, m := range msgs {
		flushed.WriteString(m.Data)
	}
	if len(msgs) < 1 || flushed.Len() != outputRawCap {
		t.Fatalf("expected immediate flush on raw cap, got %d messages / %d bytes", len(msgs), flushed.Len())
	}
}

// ANSI-dense output is split by measured marshal size: every emitted message
// fits the secure payload budget, the concatenation is lossless, and the
// seq/n coverage reconstructs the byte stream exactly the way the app
// consumes it (n>=1 ranges advance the watermark, n=0 continuations do not).
func TestOutputBatcherSplitsANSIDenseOutput(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b, r := newTestBatcher(clock.Now, always(false))

	// SGR-heavy write: 11 raw bytes per escape, marshals ~1.5x.
	// Plus one pure-ESC write to force the pathological 6x blowup path.
	sgr := strings.Repeat("\x1b[38;5;196m", 4000) // 44KB
	esc := strings.Repeat("\x1b", 12000)          // 12KB -> marshals ~72KB
	var want strings.Builder
	seq := uint64(0)
	for _, chunk := range []string{sgr, esc, "tail text"} {
		seq++
		want.WriteString(chunk)
		b.Add("s1", []byte(chunk), seq)
	}
	b.FlushAll()

	msgs := r.snapshot()
	if len(msgs) < 3 {
		t.Fatalf("expected the oversized writes to be split, got %d messages", len(msgs))
	}

	budget := int(SecureMaxTextPayload) - outputChunkMargin
	var rebuilt strings.Builder
	watermark := uint64(0)
	sawContinuation := false
	for i, m := range msgs {
		if m.Type != TypeOutput || m.SessionID != "s1" {
			t.Fatalf("msg %d: unexpected envelope %+v", i, m)
		}
		wire, err := json.Marshal(&Message{
			Type: TypeOutput, SessionID: m.SessionID, Data: m.Data, Seq: m.Seq, Outputs: m.Outputs,
		})
		if err != nil {
			t.Fatalf("msg %d: marshal: %v", i, err)
		}
		if len(wire) > budget {
			t.Fatalf("msg %d: marshalled %d bytes > budget %d", i, len(wire), budget)
		}
		switch {
		case m.Outputs >= 1:
			first := m.Seq - uint64(m.Outputs) + 1
			if first != watermark+1 {
				t.Fatalf("msg %d: range [%d,%d] does not continue watermark %d", i, first, m.Seq, watermark)
			}
			watermark = m.Seq
		case m.Outputs == 0:
			sawContinuation = true
			if m.Seq != watermark {
				t.Fatalf("msg %d: continuation seq %d != watermark %d", i, m.Seq, watermark)
			}
		default:
			t.Fatalf("msg %d: negative Outputs %d", i, m.Outputs)
		}
		rebuilt.WriteString(m.Data)
	}
	if !sawContinuation {
		t.Fatal("expected at least one n=0 continuation fragment from the pure-ESC write")
	}
	if rebuilt.String() != want.String() {
		t.Fatalf("lossy split: rebuilt %d bytes, want %d", rebuilt.Len(), want.Len())
	}
	if watermark != seq {
		t.Fatalf("final watermark %d, want %d", watermark, seq)
	}
}

// a seq discontinuity (writes dropped between subscription checks) flushes
// the segment synchronously so every emitted batch covers a contiguous range.
func TestOutputBatcherSeqDiscontinuity(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b, r := newTestBatcher(clock.Now, always(false))

	b.Add("s1", []byte("a"), 1)
	b.Add("s1", []byte("b"), 2)
	b.Add("s1", []byte("d"), 4) // 3 was dropped -> flush [1,2] first

	msgs := r.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("discontinuity should have flushed the first segment, got %d messages", len(msgs))
	}
	if msgs[0].Outputs != 2 || msgs[0].Seq != 2 {
		t.Fatalf("first segment = [%d..%d], want [1..2]", msgs[0].Seq-uint64(msgs[0].Outputs)+1, msgs[0].Seq)
	}

	b.FlushAll()
	msgs = r.snapshot()
	if len(msgs) != 2 {
		t.Fatalf("expected the post-gap segment as a second message, got %d", len(msgs))
	}
	if msgs[1].Outputs != 1 || msgs[1].Seq != 4 || msgs[1].Data != "d" {
		t.Fatalf("second segment = %+v, want n=1 seq=4 data=d", msgs[1])
	}
}

// sendJSON flushes the batch before dispatching the control message — the
// ordering contract that keeps session_exit / history_start behind output.
func TestSendJSONFlushesBatchedOutput(t *testing.T) {
	r := &rec{}
	d := &Daemon{}
	d.outBatcher = newOutputBatcher(outputBatcherDeps{send: r.send})
	d.outBatcher.Add("s1", []byte("pending"), 1)

	// The pong itself is dropped (no relay attached to the bare Daemon), but
	// the flush must have dispatched the batched output first.
	d.sendJSON(&Message{Type: TypePong})

	msgs := r.snapshot()
	if len(msgs) != 1 || msgs[0].Type != TypeOutput {
		t.Fatalf("expected the batch to be flushed before the control message, got %+v", msgs)
	}
}

// SealAt flushes entries up to the history seq and leaves the remainder as a
// separate segment — a batch must never straddle a history snapshot.
func TestOutputBatcherSealAt(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b, r := newTestBatcher(clock.Now, always(false))

	b.Add("s1", []byte("a"), 1)
	b.Add("s1", []byte("b"), 2)
	b.Add("s1", []byte("c"), 3)
	b.Add("s1", []byte("d"), 4)

	b.SealAt(func() uint64 { return 2 })

	msgs := r.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("expected only the <=seq segment flushed, got %d messages", len(msgs))
	}
	if msgs[0].Outputs != 2 || msgs[0].Seq != 2 || msgs[0].Data != "ab" {
		t.Fatalf("sealed segment = %+v, want n=2 seq=2 data=ab", msgs[0])
	}

	// the >seq remainder stays buffered until a later flush
	clock.Advance(outputIdleFlush)
	b.tick()
	msgs = r.snapshot()
	if len(msgs) != 2 {
		t.Fatalf("expected the remainder flushed later, got %d messages", len(msgs))
	}
	if msgs[1].Outputs != 2 || msgs[1].Seq != 4 || msgs[1].Data != "cd" {
		t.Fatalf("remainder segment = %+v, want n=2 seq=4 data=cd", msgs[1])
	}
}

// Reset drops buffered entries without dispatching them.
func TestOutputBatcherReset(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	b, r := newTestBatcher(clock.Now, always(false))

	b.Add("s1", []byte("stale"), 1)
	b.Reset()
	b.FlushAll()

	if n := len(r.snapshot()); n != 0 {
		t.Fatalf("Reset must drop buffered entries, got %d messages", n)
	}
}

// the daemon-level interactive window: markInput arms the interactive tier
// and it decays back to idle after outputInteractiveWindow.
func TestDaemonInteractiveWindow(t *testing.T) {
	d := &Daemon{}
	if d.interactiveOutput() {
		t.Fatal("no input yet: must be idle tier")
	}
	d.markInput()
	if !d.interactiveOutput() {
		t.Fatal("right after input: must be interactive tier")
	}
	d.lastInputAt.Store(time.Now().UnixMilli() - outputInteractiveWindow.Milliseconds() - 1)
	if d.interactiveOutput() {
		t.Fatal("after the window: must fall back to idle tier")
	}
}
