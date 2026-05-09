package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// Logger daemon global logger interface
// CLI version uses standard log package, GUI version uses RingBuffer + window output
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Writer() io.Writer
}

// StdLogger implementation based on Go standard log package (CLI version)
type StdLogger struct {
	logger *log.Logger
}

func NewStdLogger() *StdLogger {
	return &StdLogger{
		logger: log.New(os.Stderr, "", log.LstdFlags|log.Lshortfile),
	}
}

func (l *StdLogger) Debugf(format string, args ...any) { l.logger.Printf(format, args...) }
func (l *StdLogger) Infof(format string, args ...any)  { l.logger.Printf(format, args...) }
func (l *StdLogger) Warnf(format string, args ...any)   { l.logger.Printf(format, args...) }
func (l *StdLogger) Errorf(format string, args ...any)  { l.logger.Printf(format, args...) }
func (l *StdLogger) Fatalf(format string, args ...any)  { l.logger.Fatalf(format, args...) }
func (l *StdLogger) Writer() io.Writer                   { return os.Stderr }

// logPrintf backward compat: forward log.Printf style calls to Logger
// prefix parameter is automatically prepended to format
func logPrintf(l Logger, prefix, format string, args ...any) {
	if prefix != "" {
		format = prefix + " " + format
	}
	l.Infof(format, args...)
}

// logFatalf backward compat: forward log.Fatalf style calls to Logger
func logFatalf(l Logger, prefix, format string, args ...any) {
	if prefix != "" {
		format = prefix + " " + format
	}
	l.Fatalf(format, args...)
}

// logDebugf debug log (P2P, CHAN and other verbose output)
func logDebugf(l Logger, prefix, format string, args ...any) {
	if prefix != "" {
		format = prefix + " " + format
	}
	l.Debugf(format, args...)
}

// logWarnf warning log
func logWarnf(l Logger, prefix, format string, args ...any) {
	if prefix != "" {
		format = prefix + " " + format
	}
	l.Warnf(format, args...)
}

// logErrorf error log (non-fatal)
func logErrorf(l Logger, prefix, format string, args ...any) {
	if prefix != "" {
		format = prefix + " " + format
	}
	l.Errorf(format, args...)
}

// ensure StdLogger satisfies Logger
var _ Logger = (*StdLogger)(nil)

// suppress unused warning for fmt
var _ = fmt.Sprintf

// ============================================================
// FileLogger — log to file in background mode
// ============================================================

const logFileName = "daemon.log"

// logFilePath return log file path: ~/.clianywhere/daemon.log
func logFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return logFileName
	}
	return filepath.Join(home, accessKeyDir, logFileName)
}

// FileLogger based on Go standard log package, output to file
type FileLogger struct {
	logger *log.Logger
	file   *os.File
}

func newFileLogger() *FileLogger {
	if err := ensureDir(); err != nil {
		fatalExit("failed to create %s directory: %v", accessKeyDir, err)
	}
	path := logFilePath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fatalExit("failed to open log file %s: %v", path, err)
	}
	return &FileLogger{
		logger: log.New(f, "", log.LstdFlags|log.Lshortfile),
		file:   f,
	}
}

func (l *FileLogger) Debugf(format string, args ...any) { l.logger.Printf(format, args...) }
func (l *FileLogger) Infof(format string, args ...any)  { l.logger.Printf(format, args...) }
func (l *FileLogger) Warnf(format string, args ...any)   { l.logger.Printf(format, args...) }
func (l *FileLogger) Errorf(format string, args ...any)  { l.logger.Printf(format, args...) }
func (l *FileLogger) Fatalf(format string, args ...any)  { l.logger.Fatalf(format, args...) }
func (l *FileLogger) Writer() io.Writer                   { return l.file }

// ensure FileLogger satisfies Logger
var _ Logger = (*FileLogger)(nil)
