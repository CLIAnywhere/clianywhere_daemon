package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	traceMu            sync.Mutex
	traceFile          *os.File
	traceOnce          sync.Once
	traceSuffixOverride string // when non-empty, prefer this suffix (e.g. "_gui")
)

func traceInit() *os.File {
	traceOnce.Do(func() {
		home, _ := os.UserHomeDir()
		suffix := traceSuffixOverride
		if suffix == "" {
			if len(os.Args) >= 2 && os.Args[1] == "attach" {
				suffix = "_attach"
			}
		}
		path := home + "/daemon_trace" + suffix + ".log"
		var err error
		traceFile, err = os.Create(path)
		if err != nil {
			return
		}
		fmt.Fprintf(traceFile, "=== trace started pid=%d %s ===\n", os.Getpid(), time.Now().Format(time.RFC3339))
		traceFile.Sync()
	})
	return traceFile
}

func traceLog(format string, args ...interface{}) {
	f := traceInit()
	if f == nil {
		return
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	fmt.Fprintf(f, "[%s] ", time.Now().Format("15:04:05.000000"))
	fmt.Fprintf(f, format, args...)
	fmt.Fprintf(f, "\n")
}

// attachLog writes attach-specific diagnostics to ~/.clianywhere/attach.log
var (
	attachLogFile *os.File
	attachLogOnce sync.Once
	attachLogMu   sync.Mutex
)

func attachLog(format string, args ...interface{}) {
	attachLogOnce.Do(func() {
		home, _ := os.UserHomeDir()
		dir := filepath.Join(home, ".clianywhere")
		os.MkdirAll(dir, 0700)
		path := filepath.Join(dir, "attach.log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return
		}
		attachLogFile = f
		fmt.Fprintf(f, "\n=== attach_log opened pid=%d %s ===\n", os.Getpid(), time.Now().Format(time.RFC3339))
		f.Sync()
	})
	if attachLogFile == nil {
		return
	}
	attachLogMu.Lock()
	defer attachLogMu.Unlock()
	fmt.Fprintf(attachLogFile, "[%s] [%d] ", time.Now().Format("15:04:05.000000"), os.Getpid())
	fmt.Fprintf(attachLogFile, format, args...)
	fmt.Fprintf(attachLogFile, "\n")
	attachLogFile.Sync()
}

func traceHex(prefix string, data []byte) {
	f := traceInit()
	if f == nil {
		return
	}
	if len(data) == 0 {
		return
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	fmt.Fprintf(f, "[%s] %s (len=%d): ", time.Now().Format("15:04:05.000000"), prefix, len(data))

	show := data
	if len(show) > 256 {
		show = show[:256]
	}
	for i, b := range show {
		if i > 0 {
			fmt.Fprintf(f, " ")
		}
		fmt.Fprintf(f, "%02x", b)
	}
	if len(data) > 256 {
		fmt.Fprintf(f, " ...")
	}
	fmt.Fprintf(f, "  |")
	for _, b := range show {
		if b >= 32 && b < 127 {
			fmt.Fprintf(f, "%c", b)
		} else {
			fmt.Fprintf(f, ".")
		}
	}
	if len(data) > 256 {
		fmt.Fprintf(f, "...")
	}
	fmt.Fprintf(f, "|\n")
}
