package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// LocalServer local WebSocket service, bound to 127.0.0.1:56300-56400
// for CLI attach / GUI Shells Tab to connect to daemon, directly operate PTY session
type LocalServer struct {
	daemon   *Daemon
	port     int
	listener net.Listener
	upgrader websocket.Upgrader
	mu       sync.Mutex
	conns    map[*websocket.Conn]bool
	done     chan struct{}
}

// StartLocalServer try to bind 127.0.0.1:56300-56400, start HTTP service on success
// return *LocalServer for daemon cleanup on shutdown
func StartLocalServer(daemon *Daemon) *LocalServer {
	for port := 56300; port <= 56400; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}

		ls := &LocalServer{
			daemon:   daemon,
			port:     port,
			listener: listener,
			upgrader: websocket.Upgrader{
				CheckOrigin: func(r *http.Request) bool { return true },
			},
			conns: make(map[*websocket.Conn]bool),
			done:  make(chan struct{}),
		}

		fmt.Printf("Local WebSocket server on ws://127.0.0.1:%d\n", port)

		go func() {
			if err := http.Serve(listener, ls); err != nil && err != http.ErrServerClosed {
				fmt.Printf("Local WebSocket server error: %v\n", err)
			}
		}()

		return ls
	}

	fmt.Println("Warning: Failed to bind local WebSocket server (56300-56400 all in use)")
	return nil
}

// Close close local WebSocket service
func (ls *LocalServer) Close() {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	select {
	case <-ls.done:
		return // already closed
	default:
		close(ls.done)
	}

	// close all connections
	for conn := range ls.conns {
		conn.Close()
	}
	ls.conns = nil

	// close listener
	if ls.listener != nil {
		ls.listener.Close()
	}
}

// ServeHTTP handle WebSocket connection
func (ls *LocalServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := ls.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// TCP_NODELAY — disable Nagle algorithm, reduce small packet latency
	if tc, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	ls.mu.Lock()
	ls.conns[conn] = true
	ls.mu.Unlock()

	defer func() {
		ls.mu.Lock()
		delete(ls.conns, conn)
		ls.mu.Unlock()
		conn.Close()
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg Message
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case TypeSessionList:
			sessions := ls.daemon.ptyMgr.ListSessions()
			resp := Message{
				Type:         TypeSessionInfo,
				SessionInfos: sessions,
			}
			writeJSON(conn, resp)

		case TypeAttach:
			sessionID := msg.SessionID
			attachLog("LocalServer: received TypeAttach sessionID=%s", sessionID)
			if sessionID == "" {
				attachLog("LocalServer: missing session_id, sending error")
				writeJSON(conn, Message{Type: TypeError, Error: "missing session_id"})
				continue
			}

			wrapper := &LocalWSConn{conn: conn, mu: &sync.Mutex{}}
			attachLog("LocalServer: calling AttachLocalSession sessionID=%s", sessionID)
			t0 := time.Now()
			if err := ls.daemon.AttachLocalSession(sessionID, wrapper); err != nil {
				attachLog("LocalServer: AttachLocalSession failed after %dms: %v", time.Since(t0).Milliseconds(), err)
				writeJSON(conn, Message{Type: TypeError, Error: err.Error()})
				continue
			}
			attachLog("LocalServer: AttachLocalSession OK, took=%dms", time.Since(t0).Milliseconds())

			// send attach_ok followed by history data
			attachLog("LocalServer: sending attach_ok sessionID=%s", sessionID)
			writeJSON(conn, Message{Type: TypeAttachOK, SessionID: sessionID})

			// read and send history
			histT0 := time.Now()
			history, _ := ls.daemon.ptyMgr.GetHistory(sessionID)
			attachLog("LocalServer: GetHistory took=%dms len=%d", time.Since(histT0).Milliseconds(), len(history))
			if len(history) > 0 {
				attachLog("LocalServer: sending history_data len=%d", len(history))
				writeJSON(conn, Message{
					Type:      TypeHistoryData,
					SessionID: sessionID,
					Data:      string(history),
				})
			}
			attachLog("LocalServer: attach flow complete, total=%dms", time.Since(t0).Milliseconds())

		case TypeDetach:
			ls.daemon.DetachLocalSession(msg.SessionID)

		case TypeInput:
			traceHex("DAEMON PTY_WRITE<<", []byte(msg.Data))
			ls.daemon.ptyMgr.Write(msg.SessionID, []byte(msg.Data))

		case TypeResize:
			if msg.Cols > 0 && msg.Rows > 0 {
				ls.daemon.ptyMgr.Resize(msg.SessionID, msg.Cols, msg.Rows)
			}

		case TypeCreateSession:
			s, err := ls.daemon.CreateSession()
			if err != nil {
				writeJSON(conn, Message{Type: TypeError, Error: err.Error()})
			} else {
				writeJSON(conn, Message{
					Type:      TypeSessionCreated,
					SessionID: s.ID,
					PID:       s.PID,
					Name:      s.Name,
				})
			}

		case TypeDestroySession:
			ls.daemon.DestroySession(msg.SessionID)

		case TypeLocalTakeover:
			ls.daemon.LocalTakeover()

		case TypeStatus:
			state := ls.daemon.GetState()
			writeJSON(conn, Message{Type: TypeStatus, Data: state})

		case TypeStop:
			writeJSON(conn, Message{Type: TypeStop, Data: "ok"})
			ls.daemon.Destroy()
			os.Exit(0)

		default:
			// ignore unknown type
		}
	}
}

// LocalWSConn wrap websocket.Conn, implement io.Writer for session output
type LocalWSConn struct {
	conn *websocket.Conn
	mu   *sync.Mutex
}

func (c *LocalWSConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.conn.WriteJSON(Message{Type: TypeOutput, Data: string(data)})
	if err != nil {
		attachLog("LocalWSConn.Write: WriteJSON error: %v (len=%d)", err, len(data))
		return 0, err
	}
	return len(data), nil
}

// SendJSON send JSON message, implement common SendJSON interface for Daemon use
func (c *LocalWSConn) SendJSON(msg interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(msg)
}

// Close close underlying WebSocket connection, implement io.Closer interface
// daemon kicks local attach via this method (replaces sending TypeKicked message)
// NOTE: must NOT acquire mu here — Write() may be stuck on broken TCP holding mu,
// causing Close() to deadlock. Instead close underlying TCP conn directly to unblock Write.
func (c *LocalWSConn) Close() error {
	traceLog("LocalWSConn.Close() called — closing underlying TCP directly (skip mu)")
	return c.conn.UnderlyingConn().Close()
}

// writeJSON helper function: write JSON message to WebSocket connection
func writeJSON(conn *websocket.Conn, msg Message) {
	conn.WriteJSON(msg)
}
