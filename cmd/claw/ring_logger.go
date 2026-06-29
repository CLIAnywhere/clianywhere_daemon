package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

// LogEntry single log entry with timestamp, level and message
type LogEntry struct {
	Timestamp int64  `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// RingLogger implements Logger interface with a thread-safe ring buffer
// and subscriber fan-out for WebSocket log streaming
type RingLogger struct {
	mu       sync.Mutex
	logger   *log.Logger
	entries  []LogEntry
	capacity int
	writeIdx int
	count    int

	subMu       sync.RWMutex
	subscribers []chan LogEntry
}

// NewRingLogger creates a RingLogger with given capacity
func NewRingLogger(capacity int) *RingLogger {
	if capacity <= 0 {
		capacity = 1000
	}
	return &RingLogger{
		logger:      log.New(os.Stderr, "", log.LstdFlags|log.Lshortfile),
		entries:     make([]LogEntry, capacity),
		capacity:    capacity,
	}
}

func (l *RingLogger) append(level, format string, args ...any) {
	entry := LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Level:     level,
		Message:   fmt.Sprintf(format, args...),
	}

	l.mu.Lock()
	l.entries[l.writeIdx] = entry
	l.writeIdx = (l.writeIdx + 1) % l.capacity
	if l.count < l.capacity {
		l.count++
	}
	l.mu.Unlock()

	// fan-out to subscribers (non-blocking)
	l.subMu.RLock()
	defer l.subMu.RUnlock()
	for _, ch := range l.subscribers {
		select {
		case ch <- entry:
		default:
			// backpressure: drop oldest from channel
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- entry:
			default:
			}
		}
	}
}

func (l *RingLogger) Debugf(format string, args ...any) {
	l.append("DEBUG", format, args...)
	l.logger.Printf(format, args...)
}
func (l *RingLogger) Infof(format string, args ...any) {
	l.append("INFO", format, args...)
	l.logger.Printf(format, args...)
}
func (l *RingLogger) Warnf(format string, args ...any) {
	l.append("WARN", format, args...)
	l.logger.Printf(format, args...)
}
func (l *RingLogger) Errorf(format string, args ...any) {
	l.append("ERROR", format, args...)
	l.logger.Printf(format, args...)
}
func (l *RingLogger) Fatalf(format string, args ...any) {
	l.append("FATAL", format, args...)
	l.logger.Fatalf(format, args...)
}
func (l *RingLogger) Writer() io.Writer { return os.Stderr }

// GetRecent returns the last n log entries (or all if n > count)
func (l *RingLogger) GetRecent(n int) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	if n > l.count {
		n = l.count
	}
	if n == 0 {
		return nil
	}

	result := make([]LogEntry, n)
	// readIdx is where the oldest entry is
	readIdx := (l.writeIdx - l.count + l.capacity) % l.capacity
	for i := 0; i < n; i++ {
		result[i] = l.entries[(readIdx + i) % l.capacity]
	}
	return result
}

// Subscribe returns a channel that receives new log entries.
// The channel has a buffer of 256 entries. On overflow, oldest are dropped.
func (l *RingLogger) Subscribe() <-chan LogEntry {
	ch := make(chan LogEntry, 256)
	l.subMu.Lock()
	l.subscribers = append(l.subscribers, ch)
	l.subMu.Unlock()
	return ch
}

// Unsubscribe removes a previously subscribed channel
func (l *RingLogger) Unsubscribe(ch <-chan LogEntry) {
	l.subMu.Lock()
	defer l.subMu.Unlock()
	for i, sub := range l.subscribers {
		if sub == ch {
			l.subscribers = append(l.subscribers[:i], l.subscribers[i+1:]...)
			close(sub)
			return
		}
	}
}

// ensure RingLogger satisfies Logger
var _ Logger = (*RingLogger)(nil)
