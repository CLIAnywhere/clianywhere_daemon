package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// ptyConn abstract platform-specific PTY operations (separate Unix/Windows implementations)
type ptyConn interface {
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Close() error
	Resize(cols, rows int) error
	Wait() error
}

// Session single PTY session
type Session struct {
	ID        string
	pty       ptyConn
	PID       int
	Shell     string
	Name      string
	Cols      int
	Rows      int
	CreatedAt int64
	History   *Terminal // xterm-go terminal state machine, maintains logical lines and scrollback

	// multiple local controllers (browser attach), keyed by clientID
	Controllers   map[string]io.WriteCloser
	controllersMu sync.Mutex

	done chan struct{}
	once sync.Once
}

// AddController register a local controller (browser attach)
func (s *Session) AddController(clientID string, c io.WriteCloser) {
	s.controllersMu.Lock()
	s.Controllers[clientID] = c
	s.controllersMu.Unlock()
}

// RemoveController remove a local controller from this session.
// Does NOT close the underlying connection — the connection may still be
// attached to other sessions. Connection lifecycle is managed by the caller.
func (s *Session) RemoveController(clientID string) {
	s.controllersMu.Lock()
	delete(s.Controllers, clientID)
	s.controllersMu.Unlock()
}

// CloseAllControllers remove all local controllers from this session.
// Does NOT close connections — they may be attached to other sessions.
func (s *Session) CloseAllControllers() {
	s.controllersMu.Lock()
	defer s.controllersMu.Unlock()
	for id := range s.Controllers {
		delete(s.Controllers, id)
	}
}

// Broadcast send data to all local controllers, ignore individual write errors
func (s *Session) Broadcast(data []byte) {
	s.controllersMu.Lock()
	defer s.controllersMu.Unlock()
	for _, c := range s.Controllers {
		c.Write(data)
	}
}

// Close close session
func (s *Session) Close() {
	s.once.Do(func() {
		s.pty.Close()
		close(s.done)
	})
}

// PTYManager PTY session manager
type PTYManager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	order    []string // ordered session ID list, ensures ListSessions returns consistent order
	cfg      *Config
	logger   Logger

	nameCounter int

	// callback
	OnData func(sessionID string, data []byte, seq uint64)
	OnExit func(sessionID string, exitCode int)
}

// NewPTYManager create PTY manager
func NewPTYManager(cfg *Config, logger Logger) *PTYManager {
	return &PTYManager{
		sessions: make(map[string]*Session),
		cfg:      cfg,
		logger:   logger,
	}
}

// CreatePool create session pool in batch
func (pm *PTYManager) CreatePool(count int, shell string) []*Session {
	var results []*Session
	for i := 0; i < count; i++ {
		id := pm.generateID()
		s, err := pm.Create(id, shell, pm.cfg.DefaultCols, pm.cfg.DefaultRows, true)
		if err != nil {
			continue
		}
		results = append(results, s)
	}
	return results
}

// Create create new PTY session
func (pm *PTYManager) Create(id, shell string, cols, rows int, loginShell bool) (*Session, error) {
	if cols <= 0 {
		cols = pm.cfg.DefaultCols
	}
	if rows <= 0 {
		rows = pm.cfg.DefaultRows
	}

	shellPath := ResolveShell(shell)

	var homeDir string
	if home, err := os.UserHomeDir(); err == nil {
		homeDir = home
	}

	conn, pid, err := startPty(shellPath, homeDir, cols, rows, loginShell)
	if err != nil {
		return nil, err
	}

	s := &Session{
		ID:          id,
		pty:         conn,
		PID:         pid,
		Shell:       shell,
		Cols:        cols,
		Rows:        rows,
		CreatedAt:   time.Now().Unix(),
		History:     NewTerminal(pm.cfg.HistoryLines, cols, rows),
		Controllers: make(map[string]io.WriteCloser),
		done:        make(chan struct{}),
	}

	pm.mu.Lock()
	pm.nameCounter++
	s.Name = fmt.Sprintf("shell%d", pm.nameCounter)
	pm.sessions[id] = s
	pm.order = append(pm.order, id)
	pm.mu.Unlock()

	go pm.readLoop(s)
	go pm.waitExit(s)

	return s, nil
}

// Get get session
func (pm *PTYManager) Get(id string) *Session {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.sessions[id]
}

// Write write data to PTY
func (pm *PTYManager) Write(id string, data []byte) {
	pm.mu.Lock()
	s := pm.sessions[id]
	pm.mu.Unlock()
	if s != nil {
		s.pty.Write(data)
	}
}

// Resize resize PTY
func (pm *PTYManager) Resize(id string, cols, rows int) {
	pm.mu.Lock()
	s := pm.sessions[id]
	pm.mu.Unlock()
	if s != nil {
		// Skip no-op resizes to avoid triggering a PTY redraw that would
		// make the remote app receive duplicate content.
		if s.Cols == cols && s.Rows == rows {
			return
		}
		s.pty.Resize(cols, rows)
		// Recover from potential xterm reflow panics to prevent daemon crash.
		// If resize panics, rebuild History with new dimensions (scrollback lost for this session).
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.History = NewTerminal(pm.cfg.HistoryLines, cols, rows)
				}
			}()
			s.History.Resize(cols, rows)
		}()
		s.Cols = cols
		s.Rows = rows
	}
}

// Destroy destroy session
func (pm *PTYManager) Destroy(id string) {
	pm.mu.Lock()
	s := pm.sessions[id]
	if s != nil {
		delete(pm.sessions, id)
		pm.removeOrder(id)
	}
	pm.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// GetHistory get session history output (serialized as SGR text + write sequence number)
func (pm *PTYManager) GetHistory(id string) ([]byte, uint64) {
	pm.mu.Lock()
	s := pm.sessions[id]
	pm.mu.Unlock()
	if s == nil {
		return nil, 0
	}
	return s.History.Read()
}

// GetHistoryAt get session history at client size (re-serialized, content wraps at target column width)
func (pm *PTYManager) GetHistoryAt(id string, cols, rows int) ([]byte, uint64) {
	pm.mu.Lock()
	s := pm.sessions[id]
	pm.mu.Unlock()
	if s == nil {
		return nil, 0
	}
	return s.History.ReadAt(cols, rows)
}

// ListSessions list all session info in creation order
func (pm *PTYManager) ListSessions() []SessionInfo {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	var infos []SessionInfo
	for _, id := range pm.order {
		if s, ok := pm.sessions[id]; ok {
			infos = append(infos, SessionInfo{
				ID:        s.ID,
				PID:       s.PID,
				Shell:     s.Shell,
				CreatedAt: s.CreatedAt,
				Name:      s.Name,
			})
		}
	}
	return infos
}

// DestroyAll destroy all sessions
func (pm *PTYManager) DestroyAll() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, s := range pm.sessions {
		s.Close()
	}
	pm.sessions = make(map[string]*Session)
	pm.order = nil
}

// readLoop read output from PTY
func (pm *PTYManager) readLoop(s *Session) {
	buf := make([]byte, 8192)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// write to history buffer
			seq := s.History.Write(data)

			// callback
			if pm.OnData != nil {
				pm.OnData(s.ID, data, seq)
			}
		}
		if err != nil {
			// PTY closed
			return
		}
	}
}

// waitExit wait for process exit
func (pm *PTYManager) waitExit(s *Session) {
	exitCode := getExitCode(s.pty.Wait())

	pm.mu.Lock()
	delete(pm.sessions, s.ID)
	pm.removeOrder(s.ID)
	pm.mu.Unlock()

	s.Close()

	if pm.OnExit != nil {
		pm.OnExit(s.ID, exitCode)
	}
}

func (pm *PTYManager) generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// removeOrder remove specified id from ordered list (caller must hold pm.mu)
func (pm *PTYManager) removeOrder(id string) {
	for i, sid := range pm.order {
		if sid == id {
			pm.order = append(pm.order[:i], pm.order[i+1:]...)
			return
		}
	}
}
