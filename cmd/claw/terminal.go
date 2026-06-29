package main

import (
	"bytes"
	"sync"
	"sync/atomic"

	xterm "github.com/CLIAnywhere/daemon/internal/xterm"
)

// Terminal wraps xterm-go, maintains terminal state and scrollback
// replaces old VT100 implementation, provides more complete VT500 terminal emulation
// mu protects all read/write operations on term/addon, ensures mutual exclusion between readLoop writes and handleHistory serialization:
// during serialization readLoop blocks on Lock -> PTY buffer full -> shell auto-pauses output (kernel backpressure)
type Terminal struct {
	mu    sync.Mutex
	term  *xterm.Terminal
	addon *xterm.SerializeAddon
	cols  int
	rows  int
	seq   uint64
}

// NewTerminal create Terminal state machine
func NewTerminal(maxLines, cols, rows int) *Terminal {
	t := xterm.New(
		xterm.WithCols(cols),
		xterm.WithRows(rows),
		xterm.WithScrollback(maxLines),
	)
	return &Terminal{
		term:  t,
		addon: xterm.NewSerializeAddon(t),
		cols:  cols,
		rows:  rows,
	}
}

// Write process PTY output data, return current write sequence number
// lock ensures mutual exclusion with Read/ReadAt, writes block during serialization
func (t *Terminal) Write(data []byte) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(data) > 0 {
		// strip ED 3 (clear scrollback) to prevent history loss from "clear" / Codex / etc.
		data = bytes.ReplaceAll(data, []byte("\033[3J"), nil)
		data = bytes.ReplaceAll(data, []byte("\033[3;J"), nil)
		t.term.Write(data)
	}
	return atomic.AddUint64(&t.seq, 1)
}

// Read serialize terminal state (scrollback + viewport), return SGR text + current seq
// during lock, readLoop Write blocks, PTY backpressure naturally pauses shell output
func (t *Terminal) Read() ([]byte, uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	data := t.addon.Serialize(nil)
	return data, atomic.LoadUint64(&t.seq)
}

// ReadAt re-serialize terminal state at target client size
// principle: serialize main terminal -> write to temp terminal (target size) -> serialize again
// long lines wrap correctly at target column width, avoiding excessive whitespace on client
// holds lock throughout, ensures no new data written during serialization
func (t *Terminal) ReadAt(targetCols, targetRows int) ([]byte, uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	seq := atomic.LoadUint64(&t.seq)

	// 1. serialize main terminal (original size)
	raw := t.addon.Serialize(nil)

	// 2. create temp terminal with target size and same amount of scrollback
	scrollback := t.term.Scrollback()
	tmp := xterm.New(
		xterm.WithCols(targetCols),
		xterm.WithRows(targetRows),
		xterm.WithScrollback(scrollback),
	)

	// 3. write original serialized data to temp terminal, content auto-reflows at new column width
	tmp.Write(raw)

	// 4. serialize temp terminal, output is content at target size
	tmpAddon := xterm.NewSerializeAddon(tmp)
	result := tmpAddon.Serialize(nil)

	tmp.Dispose()

	return result, seq
}

// Seq return current write sequence number
func (t *Terminal) Seq() uint64 {
	return atomic.LoadUint64(&t.seq)
}

// Resize resize terminal (content auto-reflows)
func (t *Terminal) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.term.Resize(cols, rows)
	t.cols = cols
	t.rows = rows
}

// Len return current buffer line count
func (t *Terminal) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.term.Buffer().Lines.Length()
}

// Reset clear all buffers
func (t *Terminal) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.term.Reset()
}
