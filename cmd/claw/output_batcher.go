package main

// Relay-mode terminal output batching.
//
// The relay is a Cloudflare Durable Object billed per incoming WS message
// (requests) plus wake-up duration. Without batching every PTY read becomes
// its own `output` message — a vim full-screen redraw alone is dozens of small
// writes, each one waking (and billing) the DO. This batcher merges output
// writes on the daemon egress path so the relay sees at most ~3.3 msg/s when
// idle and ~100 msg/s while typing, instead of hundreds.
//
// Two-tier flush interval, keyed on "recent keyboard input":
//   - interactive (input within the last second): 10ms. Echo latency stays
//     imperceptible (worst case ~10ms tick + 10ms deadline), while vim-style
//     redraw bursts are still merged — they contain no input gaps, so sending
//     them per-write would defeat the whole point.
//   - idle (top / tail -f / build output): 300ms.
//
// Only `output` is batched. Input, resize, control messages, file frames and
// history chunks keep their existing direct paths. P2P never batches — the
// DataChannel is free, so sendOutput routes around the batcher entirely.
//
// Ordering contract with the app: each emitted message carries Seq = last
// write's seq and n = number of writes merged (coverage [Seq-n+1, Seq]);
// tail fragments of a single split write carry n = 0 ("continuation", same
// Seq). The app dedups/reorders on that range (see terminal_session_manager).

import (
	"encoding/json"
	"sync"
	"time"
)

const (
	// outputInteractiveWindow is how long after the last keystroke the session
	// counts as interactive. Echo returns within ~5-50ms but a vim redraw can
	// run 100-300ms; a shorter window would let the tail of a redraw fall into
	// the 300ms tier and feel sticky.
	outputInteractiveWindow = 1 * time.Second

	// outputInteractiveFlush is the flush interval while interactive.
	outputInteractiveFlush = 10 * time.Millisecond

	// outputIdleFlush is the flush interval with no recent input (top, tail -f,
	// compile output) — latency is acceptable, message count is what matters.
	outputIdleFlush = 300 * time.Millisecond

	// outputFlushTick is the flusher goroutine resolution. 10ms so the
	// interactive deadline is honored with at most one tick of extra delay;
	// idling ticks over an empty map cost nothing.
	outputFlushTick = 10 * time.Millisecond

	// outputRawCap is the raw byte budget per session buffer. Reaching it
	// flushes early instead of waiting for the time window — under bulk output
	// (cat a large file) the buffer fills in far less than 300ms, so latency
	// stays low while message size stays near the payload budget.
	outputRawCap = 24 * 1024

	// outputChunkMargin reserves room in SecureMaxTextPayload for the JSON
	// envelope (type / session_id / seq / n) around the data, mirroring
	// historyChunkMargin.
	outputChunkMargin = 2048

	// outputChunkUnit is the growth step when searching how much of an
	// oversized write fits into a message (mirrors historyChunkUnit).
	outputChunkUnit = 4096

	// outputMarshalFreeFloor is the raw length under which a candidate payload
	// cannot possibly exceed the budget even at the worst-case json escaping
	// blowup (6 bytes per escaped byte, e.g. ESC renders as \u001b): skipping
	// small messages keeps the interactive hot path allocation-free.
	outputMarshalFreeFloor = (SecureMaxTextPayload - outputChunkMargin - 512) / 6
)

// outEntry is one PTY write queued for batching.
type outEntry struct {
	seq  uint64
	data []byte
}

// outBuf is the per-session batch segment. entries always form a contiguous
// seq range; a discontinuity (writes dropped between subscription checks)
// flushes the segment first — see outputBatcher.Add.
type outBuf struct {
	entries  []outEntry
	firstAdd time.Time
	lastSeq  uint64
	rawLen   int
}

// outputBatcherDeps are the injectable bits (tests substitute now/interactive
// and capture send).
type outputBatcherDeps struct {
	now         func() time.Time
	interactive func() bool // true when the last input is within outputInteractiveWindow
	send        func(*Message)
}

// outputBatcher buffers relay-bound output writes per session.
type outputBatcher struct {
	deps outputBatcherDeps

	// mu guards bufs only; it is never held while sending.
	mu   sync.Mutex
	bufs map[string]*outBuf

	// sendMu serializes "take entries → dispatch" so batches reach the wire in
	// the order their entries were taken. Without it a tick-flush and a
	// FlushAll running concurrently could detach segments out of order.
	// SealAt also holds it across the history snapshot for the same reason.
	sendMu sync.Mutex

	stopCh   chan struct{}
	stopOnce sync.Once
}

func newOutputBatcher(deps outputBatcherDeps) *outputBatcher {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.interactive == nil {
		deps.interactive = func() bool { return false }
	}
	if deps.send == nil {
		deps.send = func(*Message) {}
	}
	return &outputBatcher{
		deps:   deps,
		bufs:   make(map[string]*outBuf),
		stopCh: make(chan struct{}),
	}
}

// Start launches the flusher goroutine (single, 10ms ticker).
func (b *outputBatcher) Start() {
	go b.run()
}

// Stop terminates the flusher goroutine. Buffered entries are NOT flushed —
// callers that need them flushed should FlushAll first.
func (b *outputBatcher) Stop() {
	b.stopOnce.Do(func() { close(b.stopCh) })
}

func (b *outputBatcher) run() {
	ticker := time.NewTicker(outputFlushTick)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.tick()
		}
	}
}

// tick flushes every buffer whose age reached the deadline (interactive or
// idle tier) or whose raw length reached outputRawCap.
func (b *outputBatcher) tick() {
	deadline := outputIdleFlush
	if b.deps.interactive() {
		deadline = outputInteractiveFlush
	}
	now := b.deps.now()

	type flushJob struct {
		id      string
		entries []outEntry
	}

	b.sendMu.Lock()
	defer b.sendMu.Unlock()

	var jobs []flushJob
	b.mu.Lock()
	for id, buf := range b.bufs {
		if now.Sub(buf.firstAdd) >= deadline || buf.rawLen >= outputRawCap {
			jobs = append(jobs, flushJob{id: id, entries: buf.entries})
			delete(b.bufs, id)
		}
	}
	b.mu.Unlock()

	for _, job := range jobs {
		b.sendBatch(job.id, job.entries)
	}
}

// Add queues one output write. If the new seq is not contiguous with the
// segment (writes in between were dropped by the subscription / secure-code
// checks in sendOutput), the current segment is flushed synchronously first:
// every emitted batch must cover a contiguous seq range for the app-side
// dedup to stay correct.
func (b *outputBatcher) Add(sessionID string, data []byte, seq uint64) {
	if len(data) == 0 {
		return
	}

	b.mu.Lock()
	buf := b.bufs[sessionID]
	if buf != nil && len(buf.entries) > 0 && seq != buf.lastSeq+1 {
		b.mu.Unlock()
		b.FlushSession(sessionID)
		b.mu.Lock()
		buf = nil
	}
	if buf == nil {
		buf = &outBuf{firstAdd: b.deps.now()}
		b.bufs[sessionID] = buf
	}
	buf.entries = append(buf.entries, outEntry{seq: seq, data: data})
	buf.lastSeq = seq
	buf.rawLen += len(data)
	b.mu.Unlock()
}

// FlushSession synchronously flushes one session's batch.
func (b *outputBatcher) FlushSession(sessionID string) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()

	b.mu.Lock()
	buf := b.bufs[sessionID]
	if buf == nil || len(buf.entries) == 0 {
		b.mu.Unlock()
		return
	}
	entries := buf.entries
	delete(b.bufs, sessionID)
	b.mu.Unlock()

	b.sendBatch(sessionID, entries)
}

// FlushAll synchronously flushes every session's batch. Called before control
// messages (so output precedes them on the wire), before channel switches,
// and on shutdown.
func (b *outputBatcher) FlushAll() {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()

	type flushJob struct {
		id      string
		entries []outEntry
	}

	b.mu.Lock()
	if len(b.bufs) == 0 {
		b.mu.Unlock()
		return
	}
	jobs := make([]flushJob, 0, len(b.bufs))
	for id, buf := range b.bufs {
		if len(buf.entries) > 0 {
			jobs = append(jobs, flushJob{id: id, entries: buf.entries})
		}
		delete(b.bufs, id)
	}
	b.mu.Unlock()

	for _, job := range jobs {
		b.sendBatch(job.id, job.entries)
	}
}

// SealAt runs snapshot while blocking all batch dispatching, then flushes
// every entry with seq ≤ the returned value (history seq H) and leaves the
// remainder as a fresh-bounded segment. Callers use it around history
// serialization: history covers all writes ≤ H, so those must be on the wire
// before the history message (the app buffers and skips them as duplicates),
// and everything after H must start a new segment — otherwise one batch could
// straddle the snapshot and the app would replay its pre-H bytes twice
// (concatenated batch data cannot be trimmed client-side).
func (b *outputBatcher) SealAt(snapshot func() uint64) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()

	h := snapshot()

	type flushJob struct {
		id      string
		entries []outEntry
	}

	var jobs []flushJob
	b.mu.Lock()
	for id, buf := range b.bufs {
		split := len(buf.entries)
		for i, e := range buf.entries {
			if e.seq > h {
				split = i
				break
			}
		}
		if split == 0 {
			continue
		}
		jobs = append(jobs, flushJob{id: id, entries: buf.entries[:split]})
		if split == len(buf.entries) {
			delete(b.bufs, id)
		} else {
			// Copy the remainder into a fresh slice: the flushed prefix and
			// the kept suffix currently alias the same backing array.
			rest := make([]outEntry, len(buf.entries)-split)
			copy(rest, buf.entries[split:])
			rawLen := 0
			for _, e := range rest {
				rawLen += len(e.data)
			}
			buf.entries = rest
			buf.rawLen = rawLen
		}
	}
	b.mu.Unlock()

	for _, job := range jobs {
		b.sendBatch(job.id, job.entries)
	}
}

// Reset drops all buffered entries without sending. Used when the secure
// channel or peer goes away: leftover entries could not be wrapped (keys torn
// down) and the app rebuilds its view from history on reconnect anyway.
func (b *outputBatcher) Reset() {
	b.mu.Lock()
	b.bufs = make(map[string]*outBuf)
	b.mu.Unlock()
}

// sendBatch packs entries into as few output messages as the secure payload
// budget allows and dispatches them in order.
//
// Packing rules (all validated by actually marshalling — ANSI-dense output
// marshals up to 6x its raw size, so a fixed raw chunk size cannot be used):
//   - whole writes are appended greedily while the marshalled message fits;
//   - a write that does not fit is split: its first piece rides the current
//     message (counted in n, Seq = its seq) and the tail pieces become
//     "continuation" messages (n = 0, same Seq) — this also fixes the
//     pre-existing case of a single oversized read failing WrapJSON outright;
//   - n counts only writes whose first piece is in this message, so a message
//     carrying [tail-of-7, write-8] has n=1, Seq=8 — the app writes the bytes
//     in arrival order and advances its watermark to Seq.
func (b *outputBatcher) sendBatch(sessionID string, entries []outEntry) {
	budget := int(SecureMaxTextPayload) - outputChunkMargin

	var (
		data []byte
		n    int
		seq  uint64
	)

	emit := func() {
		if len(data) == 0 {
			return
		}
		b.deps.send(&Message{
			Type:      TypeOutput,
			SessionID: sessionID,
			Data:      string(data),
			Seq:       seq,
			Outputs:   n,
		})
		data = nil
		n = 0
	}

	for _, e := range entries {
		rem := e.data
		firstPiece := true // first piece of this write in whichever message it lands

		for len(rem) > 0 {
			nextN := n
			if firstPiece {
				nextN++
			}

			// Fast path: the whole remaining write fits into the open message.
			if len(data)+len(rem) <= outputMarshalFreeFloor ||
				outputFits(sessionID, append(data, rem...), nextN, e.seq, budget) {
				data = append(data, rem...)
				seq = e.seq
				n = nextN
				firstPiece = false
				break // message stays open for the next write
			}

			// Largest prefix of rem that fits alongside the current data.
			// Growth mirrors historyPlainChunks: step by outputChunkUnit,
			// halve when the marshalled form stops fitting (pure-ESC data can
			// blow the 6x escape ratio, so even one unit may not fit).
			size := outputChunkUnit
			if size > len(rem) {
				size = len(rem)
			}
			for size > 0 {
				if len(data)+size <= outputMarshalFreeFloor ||
					outputFits(sessionID, append(data, rem[:size]...), nextN, e.seq, budget) {
					break
				}
				size /= 2
			}
			if size == 0 {
				// Current message is full — close it and retry with a fresh
				// one (a single byte always fits an empty message).
				emit()
				continue
			}

			data = append(data, rem[:size]...)
			rem = rem[size:]
			seq = e.seq
			n = nextN
			firstPiece = false
			if len(rem) > 0 {
				// Write continues in the next message as n=0 fragments.
				emit()
			}
		}
	}
	emit()
}

// outputFits reports whether an output message carrying data marshals to at
// most budget bytes. Field values match the real message so the length check
// is exact up to the n/seq digit noise absorbed by outputChunkMargin.
func outputFits(sessionID string, data []byte, n int, seq uint64, budget int) bool {
	b, err := json.Marshal(&Message{
		Type:      TypeOutput,
		SessionID: sessionID,
		Data:      string(data),
		Seq:       seq,
		Outputs:   n,
	})
	return err == nil && len(b) <= budget
}
