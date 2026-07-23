//go:build web

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

	// subcommand dispatch
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "send":
			handleSend()
			return
		case "version":
			ensureConsole()
			fmt.Printf("daemon_go %s\n", Version)
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
			fmt.Printf("'%s' is not supported yet\n", os.Args[1])
			os.Exit(1)
		}
	}

	// no args: Web mode startup
	writeEarlyLog("claw-web starting")

	// 1. Check if another instance is already running
	if isLocked() {
		writeEarlyLog("another instance already running")
		webPort := loadWebappPort()
		if webPort == -1 {
			webPort = 17900
		}
		url := fmt.Sprintf("http://127.0.0.1:%d", webPort)
		fmt.Printf("Already running: %s\n", url)
		openBrowser(url)
		return
	}

	// 2. Fork to background (Unix only, no-op on Windows)
	//    Parent process will wait for port from child, then exit.
	//    Child process continues below.
	childPort := forkToBackground()
	if childPort > 0 {
		// Parent process: child reported its port
		url := fmt.Sprintf("http://127.0.0.1:%d", childPort)
		fmt.Printf("Web terminal starting at %s\n", url)
		openBrowser(url)
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
	webPort, err := startWebAppServer(17900, 300)
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
	if err := openBrowser(url); err != nil {
		fmt.Printf("Please open this URL manually: %s\n", url)
	}

	waitForSignal(d, logger)
}
