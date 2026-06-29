//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

const lockFile = "daemon.lock"

var (
	procLockFileEx = kernel32.NewProc("LockFileEx")
)

const (
	LOCKFILE_EXCLUSIVE_LOCK    = 2
	LOCKFILE_FAIL_IMMEDIATELY  = 1
)

// tryLock attempts to acquire an exclusive non-blocking file lock on ~/.clianywhere/daemon.lock.
// Returns the file (caller must keep it open until process exits) or nil if already locked.
func tryLock() *os.File {
	home, err := os.UserHomeDir()
	if err != nil {
		writeEarlyLog(fmt.Sprintf("tryLock: UserHomeDir failed: %v", err))
		return nil
	}
	dir := filepath.Join(home, accessKeyDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeEarlyLog(fmt.Sprintf("tryLock: MkdirAll failed: %v", err))
		return nil
	}
	path := filepath.Join(dir, lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		writeEarlyLog(fmt.Sprintf("tryLock: OpenFile failed: %v", err))
		return nil
	}

	var overlapped syscall.Overlapped
	overlapped.Offset = 0
	overlapped.OffsetHigh = 0

	ret, _, err := procLockFileEx.Call(
		f.Fd(),
		LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ret == 0 {
		f.Close()
		writeEarlyLog(fmt.Sprintf("tryLock: LockFileEx failed (another instance?): %v", err))
		return nil
	}
	writeEarlyLog("tryLock: lock acquired")
	return f
}

// lockFilePath returns the lock file path for error messages
func lockFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, accessKeyDir, lockFile)
}

// isLocked checks if another instance holds the lock
func isLocked() bool {
	f, err := os.OpenFile(lockFilePath(), os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	var overlapped syscall.Overlapped
	ret, _, _ := procLockFileEx.Call(
		f.Fd(),
		LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	return ret == 0
}

// writeEarlyLog writes diagnostic info to the daemon log file
// Used before the main logger is initialized
func writeEarlyLog(msg string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, accessKeyDir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

// fatalExit prints error to stderr and exits.
// On Windows, waits for user to press any key before exiting.
func fatalExit(format string, args ...any) {
	ensureConsole()
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	writeEarlyLog(fmt.Sprintf("FATAL: "+format, args...))
	fmt.Fprintln(os.Stderr, "Press any key to exit...")
	bufio.NewScanner(os.Stdin).Scan()
	os.Exit(1)
}
