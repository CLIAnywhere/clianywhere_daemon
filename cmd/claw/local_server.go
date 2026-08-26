package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CLIAnywhere/clianywhere_daemon/internal/checkupdate"
)

// applyUpdateInFlight guards the update download/apply against concurrent
// requests from different web clients (reopened browser tabs).
var applyUpdateInFlight atomic.Bool

// package-level logger, set by StartLocalServer
var localWSDebugLogger Logger

// broadcastMsg a message targeted at either all connections or a specific connection
type broadcastMsg struct {
	conn *websocket.Conn // nil = broadcast to all; non-nil = specific connection
	msg  Message
}

// LocalServer local WebSocket service, bound to 127.0.0.1:56300-56400
// for CLI attach / GUI Shells Tab to connect to daemon, directly operate PTY session
type LocalServer struct {
	daemon   *Daemon
	port     int
	listener net.Listener
	server   *http.Server
	upgrader websocket.Upgrader
	mu       sync.Mutex
	conns    map[*websocket.Conn]bool
	done     chan struct{}
	logger   Logger

	// ring buffer logger for web UI log streaming (nil in CLI mode)
	ringLogger *RingLogger

	// broadcast channel: all writes to WebSocket connections go through this channel
	// to avoid concurrent write issues (gorilla/websocket requires one writer at a time)
	broadcastCh chan broadcastMsg

	// multi-client multi-session support: track which sessions each client is attached to
	clientIDCounter  int
	clientIDMu       sync.Mutex
	clientSessions   map[string]map[string]bool // clientID -> set of sessionIDs
	clientSessionsMu sync.Mutex

	// browser-launch watch: when a caller opens the system browser, it notifies
	// the daemon (see StartBrowserWatch); if no local client shows up within
	// the window, the daemon assumes the browser failed to open and shows a
	// native alert carrying the URL (for no-console users).
	// Both fields are guarded by mu.
	lastConnAt      time.Time // time of the most recent WS connection
	browserWatchURL string    // URL of the pending watch ("" = none)
}

// browserWatchTimeout how long to wait for a local attach after the browser
// was launched before warning the user. Generous on purpose: cold-starting a
// browser (loading profile, HDD) can easily take several seconds.
const browserWatchTimeout = 8 * time.Second

// StartBrowserWatch records that a browser was just opened for url and starts
// (or restarts) the attach watch. On timeout, if no local client is connected
// and none connected since the watch started, shows a native alert with the
// URL so the user can still reach the web terminal manually.
func (ls *LocalServer) StartBrowserWatch(url string) {
	start := time.Now()
	ls.mu.Lock()
	ls.browserWatchURL = url
	ls.mu.Unlock()

	go func() {
		time.Sleep(browserWatchTimeout)

		ls.mu.Lock()
		current := ls.browserWatchURL
		ls.browserWatchURL = ""
		connected := len(ls.conns) > 0 || ls.lastConnAt.After(start)
		ls.mu.Unlock()

		// a newer watch superseded this one
		if current != url {
			return
		}
		if !connected {
			ls.logger.Warnf("[BROWSER_WATCH] no local client attached within %s of browser launch", browserWatchTimeout)
			showAlert("CLIAnywhere",
				"The web browser may have failed to open.\nPlease visit this URL manually:\n"+url)
		}
	}()
}

// StartLocalServer try to bind 127.0.0.1:56300-56400, start HTTP service on success
// return *LocalServer for daemon cleanup on shutdown
// ringLogger may be nil (CLI mode — no log streaming)
func StartLocalServer(daemon *Daemon, logger Logger, ringLogger *RingLogger) *LocalServer {
	for port := 56300; port <= 56400; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}

		ls := &LocalServer{
			daemon:         daemon,
			port:           port,
			listener:       listener,
			upgrader:       websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
			conns:          make(map[*websocket.Conn]bool),
			clientSessions: make(map[string]map[string]bool),
			done:           make(chan struct{}),
			logger:         logger,
			ringLogger:     ringLogger,
			broadcastCh:    make(chan broadcastMsg, 256),
		}
		ls.server = &http.Server{Handler: ls}

		localWSDebugLogger = logger
		logger.Infof("Local WebSocket server on ws://127.0.0.1:%d\n", port)

		go func() {
			if err := ls.server.Serve(listener); err != nil && err != http.ErrServerClosed {
				ls.logger.Infof("Local WebSocket server error: %v\n", err)
			}
		}()

		// start broadcast writer goroutine — single writer for all connections
		go ls.broadcastLoop()

		return ls
	}

	if localWSDebugLogger != nil {
		localWSDebugLogger.Warnf("Failed to bind local WebSocket server (56300-56400 all in use)")
	}
	return nil
}

// broadcastLoop single goroutine that handles all WebSocket writes.
// This ensures gorilla/websocket's one-writer-at-a-time constraint is always satisfied.
func (ls *LocalServer) broadcastLoop() {
	for bm := range ls.broadcastCh {
		data, err := json.Marshal(bm.msg)
		if err != nil {
			continue
		}
		if bm.conn != nil {
			// targeted send
			bm.conn.WriteMessage(websocket.TextMessage, data)
		} else {
			// broadcast to all
			ls.mu.Lock()
			for conn := range ls.conns {
				conn.WriteMessage(websocket.TextMessage, data)
			}
			ls.mu.Unlock()
		}
	}
}

// send writes a message to a specific connection (via broadcast goroutine)
func (ls *LocalServer) send(conn *websocket.Conn, msg Message) {
	ls.broadcastCh <- broadcastMsg{conn: conn, msg: msg}
}

// broadcast sends a message to all connected clients (via broadcast goroutine)
func (ls *LocalServer) broadcast(msg Message) {
	select {
	case ls.broadcastCh <- broadcastMsg{conn: nil, msg: msg}:
	default:
		// channel full, drop (should not happen in practice)
	}
}

// PushState push daemon connection state to all connected clients
// called from daemon.notifyState()
func (ls *LocalServer) PushState(state, fullKey string) {
	ls.broadcast(Message{
		Type:      TypeServerStatus,
		Data:      state,
		AccessKey: fullKey,
	})
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

	// close broadcast channel (stops broadcastLoop goroutine)
	close(ls.broadcastCh)

	// close all connections
	for conn := range ls.conns {
		conn.Close()
	}
	ls.conns = nil

	// gracefully shutdown HTTP server (returns http.ErrServerClosed, suppressed in goroutine)
	if ls.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ls.server.Shutdown(ctx)
	}
}

// ServeHTTP handle WebSocket connection
func (ls *LocalServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := ls.upgrader.Upgrade(w, r, nil)
	if err != nil {
		ls.logger.Infof("[LOCAL_WS] Upgrade failed: %v", err)
		return
	}
	// TCP_NODELAY — disable Nagle algorithm, reduce small packet latency
	if tc, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	// generate unique client ID for this connection
	ls.clientIDMu.Lock()
	ls.clientIDCounter++
	clientID := fmt.Sprintf("local_%d", ls.clientIDCounter)
	ls.clientIDMu.Unlock()

	ls.mu.Lock()
	ls.conns[conn] = true
	ls.lastConnAt = time.Now()
	ls.mu.Unlock()

	// Windows localweb only: on the first browser connection, ask the user
	// whether to create a desktop shortcut. Browser WS clients always send an
	// Origin header (e.g. "http://127.0.0.1:17900"); CLI attach does not —
	// use that to avoid spamming the question at a non-interactive client.
	if origin := r.Header.Get("Origin"); origin != "" && shouldAskShortcut() {
		ls.send(conn, Message{Type: TypeAskShortcut})
	}

	defer func() {
		// detach from all sessions on disconnect
		ls.clientSessionsMu.Lock()
		sessions := ls.clientSessions[clientID]
		delete(ls.clientSessions, clientID)
		ls.clientSessionsMu.Unlock()

		for sid := range sessions {
			controllerKey := clientID + ":" + sid
			ls.daemon.DetachLocalSession(sid, controllerKey)
		}

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
			ls.send(conn, Message{
				Type:         TypeSessionInfo,
				SessionInfos: sessions,
				Shells:       DetectShells(),
			})

		case TypeAttach:
			sessionID := msg.SessionID
			if sessionID == "" {
				ls.send(conn, Message{Type: TypeError, Error: "missing session_id"})
				continue
			}

			// Resize the PTY to the client's size BEFORE registering this
			// connection as a controller. On Windows a real resize makes
			// ConPTY emit a full-screen repaint; broadcasting that to a
			// freshly attached client would append a second copy of the
			// screen after the history replay. Resizing first (and briefly
			// waiting for the repaint to be absorbed into the session
			// History) means the repaint is only seen by already-attached
			// clients, and the history pushed below is re-serialized at the
			// client's size. No size (CLI attach / legacy clients) keeps the
			// previous GetHistory behavior.
			hasClientSize := msg.Cols > 0 && msg.Rows > 0
			if hasClientSize && ls.daemon.ptyMgr.ResizeIfNeeded(sessionID, msg.Cols, msg.Rows) {
				time.Sleep(200 * time.Millisecond)
			}

			// create per-session wrapper — uses broadcastCh for writes
			wrapper := &LocalWSConn{ls: ls, conn: conn, sessionID: sessionID}
			controllerKey := clientID + ":" + sessionID

			if err := ls.daemon.AttachLocalSession(sessionID, controllerKey, wrapper); err != nil {
				ls.send(conn, Message{Type: TypeError, Error: err.Error()})
				continue
			}

			// single-shell mode: detach from all other sessions before attaching new one
			ls.clientSessionsMu.Lock()
			if ls.clientSessions[clientID] != nil {
				for oldSid := range ls.clientSessions[clientID] {
					if oldSid != sessionID {
						oldKey := clientID + ":" + oldSid
						ls.daemon.DetachLocalSession(oldSid, oldKey)
						delete(ls.clientSessions[clientID], oldSid)
						ls.logger.Infof("[LOCAL_WS] auto-detach old session %s for client %s\n", oldSid, clientID)
					}
				}
			}
			if ls.clientSessions[clientID] == nil {
				ls.clientSessions[clientID] = make(map[string]bool)
			}
			ls.clientSessions[clientID][sessionID] = true
			ls.clientSessionsMu.Unlock()

			// send attach_ok followed by history data
			ls.send(conn, Message{Type: TypeAttachOK, SessionID: sessionID})

			// read and send history (re-serialized at the client size when known)
			var history []byte
			if hasClientSize {
				history, _ = ls.daemon.ptyMgr.GetHistoryAt(sessionID, msg.Cols, msg.Rows)
			} else {
				history, _ = ls.daemon.ptyMgr.GetHistory(sessionID)
			}
			if len(history) > 0 {
				ls.send(conn, Message{
					Type:      TypeHistoryData,
					SessionID: sessionID,
					Data:      string(history),
				})
			}

		case TypeDetach:
			sessionID := msg.SessionID
			controllerKey := clientID + ":" + sessionID
			ls.daemon.DetachLocalSession(sessionID, controllerKey)

			ls.clientSessionsMu.Lock()
			if ls.clientSessions[clientID] != nil {
				delete(ls.clientSessions[clientID], sessionID)
			}
			ls.clientSessionsMu.Unlock()

		case TypeInput:
			ls.daemon.ptyMgr.Write(msg.SessionID, []byte(msg.Data))

		case TypeResize:
			if msg.Cols > 0 && msg.Rows > 0 {
				ls.daemon.ptyMgr.Resize(msg.SessionID, msg.Cols, msg.Rows)
			}

		case TypeCreateSession:
			s, err := ls.daemon.CreateSession(msg.Shell, msg.Name)
			if err != nil {
				ls.send(conn, Message{Type: TypeError, Error: err.Error()})
			} else {
				ls.send(conn, Message{
					Type:      TypeSessionCreated,
					SessionID: s.ID,
					PID:       s.PID,
					Name:      s.Name,
					Shell:     s.Shell,
				})
			}

		case TypeDestroySession:
			ls.daemon.DestroySession(msg.SessionID)

		case TypeLocalTakeover:
			ls.daemon.LocalTakeover()

		case TypeGetServerStatus:
			state := ls.daemon.GetState()
			ls.send(conn, Message{
				Type:           TypeServerStatus,
				Data:           state,
				AccessKey:      ls.daemon.accessKey,
				CurrentVersion: Version,
			})

		case TypeSetAccessKey:
			key := msg.AccessKey
			if key == "" {
				ls.send(conn, Message{Type: TypeAccessKeyResult, Error: "empty accesskey", Success: false})
				continue
			}
			if err := ls.daemon.SetAccessKeyAndConnect(key); err != nil {
				ls.send(conn, Message{Type: TypeAccessKeyResult, Error: err.Error(), Success: false})
			} else {
				ls.send(conn, Message{Type: TypeAccessKeyResult, Success: true, AccessKey: ls.daemon.GetMaskedAccessKey()})
			}

		case TypeRequestBindCode:
			cfg := ls.daemon.GetConfig()
			result, err := GenerateBindCode(cfg)
			if err != nil {
				ls.send(conn, Message{Type: TypeBindCodeResult, Error: err.Error(), Success: false})
				continue
			}
			// connect to TS and send bindcode
			bindConn, err := ConnectAndSendBindCode(result.TSWSURL, result.BindCode)
			if err != nil {
				ls.send(conn, Message{Type: TypeBindCodeResult, Error: err.Error(), Success: false})
				continue
			}
			// return QR payload to client immediately
			ls.send(conn, Message{
				Type:      TypeBindCodeResult,
				Success:   true,
				QRPayload: string(result.QRPayload),
				BindCode:  result.BindCode,
			})
			// background goroutine: wait for app to scan QR and respond
			go func() {
				defer bindConn.Close()
				accesskey, deviceName, err := WaitForBindResponse(bindConn, 120*time.Second)
				if err != nil {
					ls.send(conn, Message{Type: TypeBindCodeResult, Error: err.Error(), Success: false})
					return
				}
				ls.send(conn, Message{
					Type:       TypeBindCodeAccessKey,
					AccessKey:  accesskey,
					DeviceName: deviceName,
				})
			}()

		case TypeConfirmBindCode:
			key := msg.AccessKey
			if key == "" {
				ls.send(conn, Message{Type: TypeAccessKeyResult, Error: "empty accesskey", Success: false})
				continue
			}
			if err := ls.daemon.SetAccessKeyAndConnect(key); err != nil {
				ls.send(conn, Message{Type: TypeAccessKeyResult, Error: err.Error(), Success: false})
			} else {
				ls.send(conn, Message{Type: TypeAccessKeyResult, Success: true, AccessKey: ls.daemon.GetMaskedAccessKey()})
			}

		case TypeSubscribeLogs:
			if ls.ringLogger == nil {
				continue
			}
			ch := ls.ringLogger.Subscribe()
			// send recent history first
			for _, entry := range ls.ringLogger.GetRecent(100) {
				ls.send(conn, Message{
					Type:       TypeLogData,
					LogEntries: []LogEntry{entry},
				})
			}
			// forward new entries in background
			go func() {
				for entry := range ch {
					ls.send(conn, Message{
						Type:       TypeLogData,
						LogEntries: []LogEntry{entry},
					})
				}
			}()

		case TypeUnsubscribeLogs:
			// subscribe goroutine exits when channel is closed (on disconnect)
			// placeholder for explicit unsubscribe

		case TypeStatus:
			state := ls.daemon.GetState()
			ls.send(conn, Message{Type: TypeStatus, Data: state})

		case TypeStop:
			// sync write before exit — async send via channel would be lost
			conn.WriteJSON(Message{Type: TypeStop, Data: "ok"})
			conn.Close()
			ls.daemon.Destroy()
			os.Exit(0)

		case TypeSetSecCode:
			code := msg.Data
			if !isValidSecCode(code) {
				ls.send(conn, Message{Type: TypeSetSecCodeResult, Error: "Invalid code: must be 6 digits", Success: false})
				continue
			}
			if err := SaveSecurityCode(code); err != nil {
				ls.send(conn, Message{Type: TypeSetSecCodeResult, Error: err.Error(), Success: false})
			} else {
				atomic.StoreInt32(&ls.daemon.secCodeEnabled, 1)
				atomic.StoreInt32(&ls.daemon.secCodeVerified, 0)
				ls.send(conn, Message{Type: TypeSetSecCodeResult, Success: true})
			}

		case TypeUnsetSecCode:
			if err := ClearSecurityCode(); err != nil {
				ls.send(conn, Message{Type: TypeUnsetSecCodeResult, Error: err.Error(), Success: false})
			} else {
				atomic.StoreInt32(&ls.daemon.secCodeEnabled, 0)
				ls.send(conn, Message{Type: TypeUnsetSecCodeResult, Success: true})
			}

		case TypeGetSecCodeStatus:
			ls.send(conn, Message{
				Type:    TypeSecCodeStatus,
				Success: HasSecurityCode(),
			})

		case TypeShortcutResponse:
			// User dismissed the first-connection recommend dialog (confirm
			// or cancel). Mark it asked either way so the prompt never fires
			// again; actual creation happens via set_shortcut in Settings.
			markShortcutAsked()

		case TypeGetStartupStatus:
			supported := autoRunSupported()
			ls.send(conn, Message{
				Type:              TypeStartupStatus,
				Shortcut:          hasDesktopShortcut(),
				AutoRun:           supported && hasAutoRun(),
				AutoRunSupported:  supported,
			})

		case TypeSetShortcut:
			// Any explicit user action here counts as "asked".
			markShortcutAsked()
			// Wrap in recover: a panic inside go-ole/syscall (e.g. nil
			// dispatch, malformed VARIANT) used to crash this connection
			// handler goroutine and appear as "daemon hung". Convert any
			// panic into an error result so the user sees a SnackBar.
			func() {
				defer func() {
					if r := recover(); r != nil {
						ls.logger.Errorf("[SHORTCUT] panic during set: %v", r)
						ls.send(conn, Message{
							Type:    TypeSetShortcutResult,
							Error:   fmt.Sprintf("internal panic: %v", r),
							Shortcut: hasDesktopShortcut(),
						})
					}
				}()
				var err error
				if msg.Enable {
					err = createDesktopShortcut()
				} else {
					err = removeDesktopShortcut()
				}
				if err != nil {
					ls.logger.Errorf("[SHORTCUT] set shortcut(enable=%v) failed: %v", msg.Enable, err)
					ls.send(conn, Message{Type: TypeSetShortcutResult, Error: err.Error(), Shortcut: hasDesktopShortcut()})
				} else {
					ls.logger.Infof("[SHORTCUT] shortcut %s", map[bool]string{true: "created", false: "removed"}[msg.Enable])
					ls.send(conn, Message{Type: TypeSetShortcutResult, Success: true, Shortcut: hasDesktopShortcut()})
				}
			}()

		case TypeSetAutoRun:
			var err error
			if msg.Enable {
				if !autoRunSupported() {
					err = fmt.Errorf("auto run is not supported in this environment (systemd not available)")
				} else {
					err = enableAutoRun()
				}
			} else {
				err = disableAutoRun()
			}
			if err != nil {
				ls.logger.Errorf("[AUTORUN] set autorun(enable=%v) failed: %v", msg.Enable, err)
				ls.send(conn, Message{Type: TypeSetAutoRunResult, Error: err.Error(), AutoRun: hasAutoRun()})
			} else {
				ls.logger.Infof("[AUTORUN] autorun %s", map[bool]string{true: "enabled", false: "disabled"}[msg.Enable])
				ls.send(conn, Message{Type: TypeSetAutoRunResult, Success: true, AutoRun: hasAutoRun()})
			}

		case TypeCheckUpdate:
			// async: GitHub API round-trip must not block the read loop
			go func() {
				checkupdate.SetLogger(ls.logger)
				res, err := checkupdate.CheckUpdate(Version)
				if err != nil {
					ls.send(conn, Message{Type: TypeCheckUpdateResult, Error: err.Error()})
					return
				}
				ls.send(conn, Message{
					Type:            TypeCheckUpdateResult,
					Success:         true,
					CurrentVersion:  res.Current,
					LatestVersion:   res.Latest,
					UpdateAvailable: res.Available,
				})
			}()

		case TypeApplyUpdate:
			// Self-built binaries only check — applying a prebuilt release
			// package is refused; the user rebuilds from source instead.
			if !isOfficeBuild {
				ls.send(conn, Message{Type: TypeApplyUpdateResult, Error: selfBuildNotice})
				continue
			}
			// async: download + installer launch must not block the read loop
			go func() {
				// re-entry guard: a second apply_update (e.g. user closed and
				// reopened the browser mid-download) must not run a parallel
				// download to the same .part file (corruption) or launch a
				// second installer.
				if !applyUpdateInFlight.CompareAndSwap(false, true) {
					ls.send(conn, Message{Type: TypeApplyUpdateResult, Error: "update already in progress"})
					return
				}
				defer applyUpdateInFlight.Store(false)
				checkupdate.SetLogger(ls.logger)
				res, err := checkupdate.CheckUpdate(Version)
				if err != nil {
					ls.send(conn, Message{Type: TypeApplyUpdateResult, Error: err.Error()})
					return
				}
				if !res.Available {
					ls.send(conn, Message{Type: TypeApplyUpdateResult, Success: true, Data: "already up to date"})
					return
				}
				ls.send(conn, Message{Type: TypeApplyUpdateResult, Success: true, Data: "downloading"})
				ls.logger.Infof("[UPDATE] applying update %s -> %s", res.Current, res.Latest)
				if err := checkupdate.ApplyUpdate(res); err != nil {
					ls.logger.Errorf("[UPDATE] apply failed: %v", err)
					ls.send(conn, Message{Type: TypeApplyUpdateResult, Error: err.Error()})
				} else {
					// Reached on platforms where ApplyUpdate returns on
					// success (macOS: dmg opened for manual install). On
					// Windows it never returns — the process exits for the
					// installer — so this is not sent there.
					ls.send(conn, Message{Type: TypeApplyUpdateResult, Success: true, Data: updateNoticeText()})
					exitAfterUpdate()
				}
			}()

		default:
			// ignore unknown type
		}
	}
}

// LocalWSConn wrap websocket.Conn, implement io.Writer for session output
// All writes go through the LocalServer's broadcast channel for thread safety
type LocalWSConn struct {
	ls        *LocalServer
	conn      *websocket.Conn
	sessionID string // immutable after creation, used to tag output messages
}

func (c *LocalWSConn) Write(data []byte) (int, error) {
	c.ls.send(c.conn, Message{
		Type:      TypeOutput,
		SessionID: c.sessionID,
		Data:      string(data),
	})
	return len(data), nil
}

// SendJSON send JSON message, implement common SendJSON interface for Daemon use
func (c *LocalWSConn) SendJSON(msg interface{}) error {
	m, ok := msg.(*Message)
	if !ok {
		return nil
	}
	c.ls.send(c.conn, *m)
	return nil
}

// Close close underlying WebSocket connection, implement io.Closer interface
// daemon kicks local attach via this method
// NOTE: close underlying TCP conn directly to unblock any pending writes
func (c *LocalWSConn) Close() error {
	if localWSDebugLogger != nil {
		localWSDebugLogger.Infof("[LOCAL_WS] LocalWSConn.Close() called — closing underlying TCP")
	}
	return c.conn.UnderlyingConn().Close()
}
