//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const lockFile = "daemon.lock"

// tryLock attempts to acquire an exclusive non-blocking flock on ~/.clianywhere/daemon.lock.
// Returns the file (caller must keep it open until process exits) or nil if already locked.
func tryLock() *os.File {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(home, accessKeyDir), 0700); err != nil {
		return nil
	}
	path := filepath.Join(home, accessKeyDir, lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0600)
	if err != nil {
		return nil
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		return nil
	}
	return f
}

// lockFilePath returns the lock file path for error messages
func lockFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, accessKeyDir, lockFile)
}

// isLocked checks if another instance holds the lock (without acquiring it)
func isLocked() bool {
	f, err := os.OpenFile(lockFilePath(), os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		return true // locked by another process
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// fatalExit prints error to stderr and exits.
func fatalExit(format string, args ...any) {
	ensureConsole()
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// writeEarlyLog writes diagnostic info to the daemon log file
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
