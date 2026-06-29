//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// scLog writes a shortcut diagnostic line to ~/.clianywhere/daemon.log via
// writeEarlyLog. We deliberately do NOT use the daemon's RingLogger here
// because RingLogger writes to stderr, which is invisible in GUI mode
// (-H windowsgui). writeEarlyLog appends to the same file the user already
// tails for startup diagnostics.
func scLog(format string, args ...any) {
	writeEarlyLog("[SHORTCUT] " + fmt.Sprintf(format, args...))
}

// userDesktopPath resolves the per-user Desktop directory.
// Prefers USERPROFILE\Desktop; falls back to the common desktop if that fails.
func userDesktopPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	desktop := filepath.Join(home, "Desktop")
	if fi, err := os.Stat(desktop); err == nil && fi.IsDir() {
		return desktop, nil
	}
	// OneDrive redirection: %USERPROFILE%\OneDrive\Desktop
	od := filepath.Join(home, "OneDrive", "Desktop")
	if fi, err := os.Stat(od); err == nil && fi.IsDir() {
		return od, nil
	}
	return "", fmt.Errorf("desktop directory not found")
}

// ---------------------------------------------------------------------------
// Dedicated COM worker
//
// Why a single long-lived worker goroutine instead of per-call CoInitialize:
//
// COM is thread-local AND apartment-stateful. Each per-call CoInitializeEx +
// CoUninitialize pair on a fresh thread used to work in older Windows, but on
// modern shells (especially with OneDrive / shell32 helpers cached from a
// previous apartment) the second invocation can hit an access violation
// inside IPersistFile.Save, crashing the daemon.
//
// This worker:
//   - locks one OS thread for its entire lifetime
//   - calls CoInitializeEx exactly ONCE and NEVER CoUninitialize (the
//     apartment stays alive for the daemon's lifetime — Go reclaims at exit)
//   - serializes every createDesktopShortcut request via a channel, so all
//     COM objects live on the same STA with no cross-thread marshalling
// ---------------------------------------------------------------------------

var (
	shortcutWorkerOnce sync.Once
	shortcutReqCh      = make(chan *shortcutJob, 8)
)

type shortcutJob struct {
	exePath string
	workDir string
	lnkPath string
	reply   chan error
}

// ensureShortcutWorker starts the background COM worker exactly once.
func ensureShortcutWorker() {
	shortcutWorkerOnce.Do(func() {
		go shortcutWorkerLoop()
	})
}

func shortcutWorkerLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	scLog("worker: thread locked (threadID=%d)", windowsThreadID())

	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		// S_FALSE (already initialized on this thread) is benign;
		// RPC_E_CHANGED_MODE means another apartment was set up first — we
		// log and proceed; calls will fail with a clear error rather than crash.
		scLog("worker: CoInitializeEx returned %v", err)
	} else {
		scLog("worker: STA apartment initialized")
	}
	// Intentionally NO CoUninitialize here. Keeping the apartment alive
	// prevents the shell32 cached-state corruption that crashed us before.

	for job := range shortcutReqCh {
		scLog("worker: start job lnk=%s exe=%s", job.lnkPath, job.exePath)
		err := doCreateShortcut(job)
		scLog("worker: job done err=%v", err)
		job.reply <- err
	}
}

// doCreateShortcut performs the actual COM calls. MUST be called only from
// shortcutWorkerLoop — relies on the worker's pre-initialized STA.
func doCreateShortcut(job *shortcutJob) (retErr error) {
	scLog("step 1: CreateObject WScript.Shell")
	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("create WScript.Shell: %w", err)
	}
	defer func() {
		scLog("step 5: release unknown")
		unknown.Release()
	}()

	scLog("step 2: QueryInterface IDispatch")
	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("query IDispatch: %w", err)
	}
	defer func() {
		scLog("step 5: release shell dispatch")
		shell.Release()
	}()

	scLog("step 3: CallMethod CreateShortcut(%s)", job.lnkPath)
	res, err := oleutil.CallMethod(shell, "CreateShortcut", job.lnkPath)
	if err != nil {
		return fmt.Errorf("CreateShortcut: %w", err)
	}
	defer func() {
		scLog("step 5: clear result variant")
		res.Clear()
	}()

	scDisp := res.ToIDispatch()
	if scDisp == nil {
		return fmt.Errorf("CreateShortcut returned nil dispatch")
	}
	// NOTE: do NOT defer scDisp.Release() — res.Clear() below already releases
	// the inner IDispatch via VariantClear. Releasing both double-frees the
	// object: scDisp and res share the same underlying IDispatch pointer
	// (ToIDispatch is a reinterpret cast with no AddRef). On the second call
	// to createDesktopShortcut this triggered an access violation inside
	// VariantClear on a dangling pointer, crashing the daemon.

	scLog("step 4a: set TargetPath=%s", job.exePath)
	if _, err := oleutil.PutProperty(scDisp, "TargetPath", job.exePath); err != nil {
		return fmt.Errorf("set TargetPath: %w", err)
	}
	scLog("step 4b: set WorkingDirectory=%s", job.workDir)
	if _, err := oleutil.PutProperty(scDisp, "WorkingDirectory", job.workDir); err != nil {
		return fmt.Errorf("set WorkingDirectory: %w", err)
	}
	scLog("step 4c: set IconLocation=%s,0", job.exePath)
	if _, err := oleutil.PutProperty(scDisp, "IconLocation", job.exePath+",0"); err != nil {
		return fmt.Errorf("set IconLocation: %w", err)
	}
	scLog("step 4d: CallMethod Save")
	if _, err := oleutil.CallMethod(scDisp, "Save"); err != nil {
		return fmt.Errorf("Save: %w", err)
	}
	scLog("step 4e: Save returned OK")
	return nil
}

// createDesktopShortcut is the public entry point. It resolves paths on the
// caller's goroutine, then submits the COM work to the dedicated worker.
func createDesktopShortcut() error {
	ensureShortcutWorker()

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = real
	}

	desktop, err := userDesktopPath()
	if err != nil {
		return fmt.Errorf("resolve desktop: %w", err)
	}

	exeName := filepath.Base(exePath)
	if ext := filepath.Ext(exeName); ext != "" {
		exeName = exeName[:len(exeName)-len(ext)]
	}

	job := &shortcutJob{
		exePath: exePath,
		workDir: filepath.Dir(exePath),
		lnkPath: filepath.Join(desktop, exeName+".lnk"),
		reply:   make(chan error, 1),
	}

	scLog("submit job to worker")
	shortcutReqCh <- job
	return <-job.reply
}
