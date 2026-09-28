package main

import (
	"sync"
	"testing"
	"time"
)

// Reproduces the 2026-09-27 bug: after the app cancelled a file download the
// daemon panicked with "close of closed channel", taking the whole process
// down and killing terminal echo with it.
func TestCancelThenCancelAllDoesNotPanic(t *testing.T) {
	ftm := &FileTransferManager{active: map[uint32]*FileSendTask{}}

	ftm.mu.Lock()
	ftm.active[7] = &FileSendTask{ID: 7, cancel: make(chan struct{})}
	ftm.mu.Unlock()

	// The app cancels a single file.
	ftm.Cancel(7)

	// Then a P2P drop / app-offline triggers CancelAll — the old
	// implementation panicked here.
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("CancelAll panicked: %v", r)
			}
			close(done)
		}()
		ftm.CancelAll()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CancelAll blocked")
	}

	// The task must already be removed from active; a repeated Cancel is a no-op.
	ftm.mu.Lock()
	_, still := ftm.active[7]
	ftm.mu.Unlock()
	if still {
		t.Error("task still in active after Cancel")
	}
	ftm.Cancel(7) // idempotent, must not panic
}

// Reverse order: CancelAll first then Cancel must not panic either.
func TestCancelAllThenCancelDoesNotPanic(t *testing.T) {
	ftm := &FileTransferManager{active: map[uint32]*FileSendTask{}}
	ftm.mu.Lock()
	ftm.active[9] = &FileSendTask{ID: 9, cancel: make(chan struct{})}
	ftm.mu.Unlock()

	ftm.CancelAll()
	ftm.Cancel(9)

	// The cancel channel must really be closed (the sender loop must be able
	// to exit).
	ftm.mu.Lock()
	task, ok := ftm.active[9]
	ftm.mu.Unlock()
	if ok {
		select {
		case <-task.cancel:
		default:
			t.Error("cancel channel not closed")
		}
	}
}

// Concurrent cancels must not panic (rapid app taps + disconnect events
// arriving together).
func TestConcurrentCancelNoPanic(t *testing.T) {
	ftm := &FileTransferManager{active: map[uint32]*FileSendTask{}}
	for i := uint32(1); i <= 20; i++ {
		ftm.active[i] = &FileSendTask{ID: i, cancel: make(chan struct{})}
	}

	var wg sync.WaitGroup
	for i := uint32(1); i <= 20; i++ {
		wg.Add(2)
		go func(id uint32) { defer wg.Done(); ftm.Cancel(id) }(i)
		go func() { defer wg.Done(); ftm.CancelAll() }()
	}
	wg.Wait()
}
