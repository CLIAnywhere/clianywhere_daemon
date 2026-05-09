package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	// daemonized subprocess entry: skip interaction, load cached key directly
	if os.Getenv("CLIANYWHERE_DAEMONIZED") == "1" {
		runDaemonized()
		return
	}

	logger := createLogger()

	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "send":
			handleSend()
			return
		case "version":
			ensureConsole()
			fmt.Println("daemon_go v0.2.0")
			return
		case "attach":
			ensureConsole()
			runAttachCLI()
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

	// no args: dispatch to CLI or GUI normal startup entry
	runApp(logger)
}

// handleSend handle send subcommand (shared CLI/GUI, pure CLI operation)
func handleSend() {
	args := os.Args[2:]
	if len(args) < 1 || args[0] == "" {
		log.Fatal("Usage: daemon send <filepath>")
	}
	filePath := args[0]

	sendBody, _ := json.Marshal(map[string]string{"path": filePath})
	client := &http.Client{Timeout: 5 * time.Second}

	const portMin = 56881
	const portMax = 56981
	found := false
	for port := portMin; port <= portMax; port++ {
		url := fmt.Sprintf("http://127.0.0.1:%d/send", port)
		resp, err := client.Post(url, "application/json", bytes.NewReader(sendBody))
		if err != nil {
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			fmt.Println(string(respBody))
			found = true
			break
		}
		log.Fatalf("send failed (%d): %s", resp.StatusCode, string(respBody))
	}
	if !found {
		log.Fatal("no running daemon found (ports 56881-56981 unresponsive), please run daemon run first")
	}
}

// queryDaemonStatus connect to local WS to query daemon status, returns ("", false) if not running
func queryDaemonStatus() (state string, ok bool) {
	port := findLocalWSPort()
	if port == -1 {
		return "", false
	}

	url := fmt.Sprintf("ws://127.0.0.1:%d", port)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		return "", false
	}
	defer conn.Close()

	conn.WriteJSON(Message{Type: TypeStatus})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	var msg Message
	if err := conn.ReadJSON(&msg); err != nil || msg.Type != TypeStatus {
		return "", false
	}

	return msg.Data, true
}

// handleStatus query current daemon run status
func handleStatus() {
	state, ok := queryDaemonStatus()
	if !ok {
		fmt.Println("not running")
		os.Exit(1)
	}
	fmt.Println(state)
	if state != "connected" {
		os.Exit(1)
	}
}

// handleStop send stop command via local WS to exit daemon process
func handleStop() {
	port := findLocalWSPort()
	if port == -1 {
		fmt.Println("not running")
		os.Exit(1)
	}

	url := fmt.Sprintf("ws://127.0.0.1:%d", port)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		fmt.Println("not running")
		os.Exit(1)
	}
	defer conn.Close()

	conn.WriteJSON(Message{Type: TypeStop})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	var msg Message
	if err := conn.ReadJSON(&msg); err != nil || msg.Type != TypeStop {
		fmt.Println("failed to stop daemon")
		os.Exit(1)
	}

	fmt.Println("stopped")
}

// runDaemonized daemonized subprocess entry: uses FileLogger, loads cached key to run
func runDaemonized() {
	logger := newFileLogger()
	cfg := DefaultConfig()

	d := NewDaemon("", cfg, logger)
	d.Init()

	accessKey, _ := loadAccessKey()
	if accessKey != "" {
		d.StartRemote(accessKey)
	}

	waitForSignal(d, logger)
}

// waitForSignal wait for SIGINT/SIGTERM then destroy daemon
func waitForSignal(d *Daemon, logger Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logPrintf(logger, "[daemon]", "shutting down...")
		cancel()
	}()

	<-ctx.Done()
	d.Destroy()
	logPrintf(logger, "[daemon]", "exited")
}
