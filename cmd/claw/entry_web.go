//go:build web

package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// webapp port file: ~/.clianywhere/webapp.port
// Primary instance writes its webapp port here; second instance reads it to open browser.

func saveWebappPort(port int) {
	if path, err := webappPortPath(); err == nil {
		os.WriteFile(path, []byte(strconv.Itoa(port)), 0600)
	}
}

func loadWebappPort() int {
	path, err := webappPortPath()
	if err != nil {
		return -1
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return -1
	}
	return port
}

func webappPortPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home + "/.clianywhere/webapp.port", nil
}

// notifyBrowserLaunched tells the daemon serving url that a browser was just
// opened for it, so it starts the attach watch (see StartBrowserWatch).
// Synchronous with a short timeout: the caller typically exits right after.
func notifyBrowserLaunched(url string) {
	client := &http.Client{Timeout: 2 * time.Second}
	client.Get(url + "/browser-launched") //nolint:errcheck
}

func main() {
	// prevent running inside daemon PTY (e.g. app terminal)
	if os.Getenv("IS_CLIANYWHERE_PTY") == "1" {
		fmt.Fprintln(os.Stderr, "Error: cannot run claw inside CliAnyWhere terminal")
		os.Exit(1)
	}

	// read CLIANYWHERE_TS env: skip TS selection, use specified addr directly
	if tsAddr := os.Getenv("CLIANYWHERE_TS"); tsAddr != "" {
		SetForceTSAddr(tsAddr)
	}

	// --autostart: launched by the OS autostart entry (HKCU Run key /
	// LaunchAgent / systemd user unit). Silent mode: never opens a browser
	// and exits quietly when another instance is already running.
	args := os.Args[1:]
	autostartMode := false
	if len(args) > 0 && args[0] == "--autostart" {
		autostartMode = true
		args = args[1:]
	}

	// subcommand dispatch
	if len(args) >= 1 {
		switch args[0] {
		case "send":
			handleSend()
			return
		case "version":
			ensureConsole()
			fmt.Printf("daemon_go %s\n", Version)
			return
		case "checkupdate":
			ensureConsole()
			handleCheckUpdate()
			return
		case "update":
			ensureConsole()
			handleUpdate()
			return
		case "status":
			ensureConsole()
			handleStatus()
			return
		case "stop":
			ensureConsole()
			handleStop()
			return
		default:
			ensureConsole()
			fmt.Printf("'%s' is not supported yet\n", args[0])
			os.Exit(1)
		}
	}

	// no args: Web mode startup
	writeEarlyLog("claw-web starting")

	// 1. Check if another instance is already running
	if isLocked() {
		writeEarlyLog("another instance already running")
		if autostartMode {
			// Autostart races a manual launch: the manual instance wins,
			// the autostart one exits without touching the browser.
			return
		}
		webPort := loadWebappPort()
		if webPort == -1 {
			webPort = 17900
		}
		url := fmt.Sprintf("http://127.0.0.1:%d", webPort)
		fmt.Printf("Already running: %s\n", url)
		openBrowser(url)
		notifyBrowserLaunched(url)
		return
	}

	// 2. Fork to background (Unix only, no-op on Windows)
	//    Parent process will wait for port from child, then exit.
	//    Child process continues below.
	childPort := forkToBackground()
	if childPort > 0 {
		// Parent process: child reported its port
		if autostartMode {
			// Login-triggered start: no browser, just get out of the way.
			os.Exit(0)
		}
		url := fmt.Sprintf("http://127.0.0.1:%d", childPort)
		fmt.Printf("Web terminal starting at %s\n", url)
		openBrowser(url)
		notifyBrowserLaunched(url)
		os.Exit(0)
	}

	// 3. Child process (or no-fork on Windows): acquire lock
	lock := tryLock()
	if lock == nil {
		writeEarlyLog("lock failed after fork, another instance may have started")
		return
	}
	defer lock.Close()
	writeEarlyLog("lock acquired, starting daemon")

	logger := NewRingLogger(1000)
	cfg := DefaultConfig()

	// start daemon in-process
	d := NewDaemon("", cfg, logger)
	d.Init()
	d.localServer.ringLogger = logger

	accessKey, err := loadAccessKey()
	if err != nil {
		logger.Errorf("failed to read accesskey: %v", err)
	}
	if accessKey != "" {
		d.StartRemote(accessKey)
		logger.Infof("[ts] auto-connecting with saved accesskey")
	}

	// start webapp HTTP server
	if len(webappFiles) == 0 {
		fatalExit("webapp not available (zip not embedded)")
	}
	webPort, err := startWebAppServer(17900, 300, func(u string) {
		if d.localServer != nil {
			d.localServer.StartBrowserWatch(u)
		}
	})
	if err != nil {
		fatalExit("failed to start web server: %v", err)
	}
	saveWebappPort(webPort)

	url := fmt.Sprintf("http://127.0.0.1:%d", webPort)

	// 4. Notify parent process of port (Unix only, no-op on Windows)
	notifyParentPort(webPort)

	// Windows: open browser here (Unix: parent already opened it)
	fmt.Printf("Web terminal at %s\n", url)
	writeEarlyLog(fmt.Sprintf("webapp started at %s", url))
	if autostartMode {
		// Silent start: no browser, and no browser-failure watch either —
		// StartBrowserWatch would pop a native alert after its timeout since
		// no browser client is ever expected to connect on its own here.
		writeEarlyLog("autostart mode: skipping browser open")
	} else {
		if err := openBrowser(url); err != nil {
			fmt.Printf("Please open this URL manually: %s\n", url)
		}
		if d.localServer != nil {
			d.localServer.StartBrowserWatch(url)
		}
	}

	waitForSignal(d, logger)
}
