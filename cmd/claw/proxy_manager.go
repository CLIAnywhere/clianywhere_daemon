package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ProxyManager manage all proxy tunnel connections
type ProxyManager struct {
	mu     sync.Mutex
	conns  map[uint32]*ProxyConn // connId → active proxy connection
	daemon *Daemon
	logger Logger
}

// ProxyConn single proxy TCP connection
type ProxyConn struct {
	ID        uint32
	ConnID    string // "conn_00000001" format, maps to JSON session_id
	tcpConn   net.Conn
	daemon    *Daemon
	cancel    chan struct{}
	once      sync.Once
	remoteAddr string
}

// NewProxyManager create proxy manager
func NewProxyManager(d *Daemon, logger Logger) *ProxyManager {
	return &ProxyManager{
		daemon: d,
		logger: logger,
		conns:  make(map[uint32]*ProxyConn),
	}
}

// HandleConnect handle proxy_connect message: asynchronously establish TCP connection to target
// note: TCP dial executes in goroutine, does not block caller (P2P DataChannel OnMessage)
func (pm *ProxyManager) HandleConnect(msg *Message) {
	connIDStr := msg.SessionID
	targetAddr := msg.Data // "host:port"

	logPrintf(pm.logger, "[HTTP-PROXY]", "requesting connection: connID=%s target=%s", connIDStr, targetAddr)

	// extract uint32 connId from session_id
	var connID uint32
	fmt.Sscanf(connIDStr, "conn_%08d", &connID)
	if connID == 0 {
		logPrintf(pm.logger, "[HTTP-PROXY]", "invalid connID: %s", connIDStr)
		pm.daemon.sendJSON(&Message{
			Type:      TypeProxyError,
			SessionID: connIDStr,
			Error:     "invalid connection id",
		})
		return
	}

	// goroutine: async TCP dial, does not block message processing loop
	go func() {
		tcpConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
		if err != nil {
			logPrintf(pm.logger, "[HTTP-PROXY]", "connection failed: connID=%s target=%s err=%v", connIDStr, targetAddr, err)
			pm.daemon.sendJSON(&Message{
				Type:      TypeProxyError,
				SessionID: connIDStr,
				Error:     err.Error(),
			})
			return
		}

		// register connection
		pc := &ProxyConn{
			ID:         connID,
			ConnID:     connIDStr,
			tcpConn:    tcpConn,
			daemon:     pm.daemon,
			cancel:     make(chan struct{}),
			remoteAddr: tcpConn.RemoteAddr().String(),
		}

		pm.mu.Lock()
		pm.conns[connID] = pc
		pm.mu.Unlock()

		// notify app connection success
		pm.daemon.sendJSON(&Message{
			Type:      TypeProxyConnected,
			SessionID: connIDStr,
		})

		logPrintf(pm.logger, "[HTTP-PROXY]", "connection successful: connID=%s target=%s remoteAddr=%s", connIDStr, targetAddr, pc.remoteAddr)

		// start readLoop: read data from TCP connection and send to app
		pm.readLoop(pc)
	}()
}

// readLoop continuously read data from TCP connection, construct binary frames and send to app
func (pm *ProxyManager) readLoop(pc *ProxyConn) {
	buf := make([]byte, MaxFrameSize-5) // 5-byte proxy data header
	defer pm.removeConn(pc.ID)

	for {
		select {
		case <-pc.cancel:
			logPrintf(pm.logger, "[HTTP-PROXY]", "readLoop exited due to cancel: connID=%s remote=%s", pc.ConnID, pc.remoteAddr)
			return
		default:
		}

		n, err := pc.tcpConn.Read(buf)
		if n > 0 {
			frameSize := 5 + n
			if frameSize > 31*1024 {
				logPrintf(pm.logger, "[HTTP-PROXY]", "WARNING: frame too large: %d bytes (connID=%s)", frameSize, pc.ConnID)
			}
			// construct binary frame: [0x05][4B connId BE][payload...]
			frame := make([]byte, 5+n)
			frame[0] = OpcodeProxyData
			binary.BigEndian.PutUint32(frame[1:5], pc.ID)
			copy(frame[5:], buf[:n])
			pc.daemon.sendBytes(frame)
		}
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				logPrintf(pm.logger, "[HTTP-PROXY]", "connection closed locally: connID=%s remote=%s（possibly closed by app side）", pc.ConnID, pc.remoteAddr)
			} else if n == 0 {
				logPrintf(pm.logger, "[HTTP-PROXY]", "connection closed: connID=%s remote=%s err=%v", pc.ConnID, pc.remoteAddr, err)
			} else {
				logPrintf(pm.logger, "[HTTP-PROXY]", "read error: connID=%s remote=%s err=%v", pc.ConnID, pc.remoteAddr, err)
			}
			// notify app connection closed
			pc.daemon.sendJSON(&Message{
				Type:      TypeProxyClose,
				SessionID: pc.ConnID,
			})
			return
		}
	}
}

// HandleData handle tunnel data frame from app (header stripped)
func (pm *ProxyManager) HandleData(connID uint32, data []byte) {
	pm.mu.Lock()
	pc, ok := pm.conns[connID]
	pm.mu.Unlock()

	if !ok {
		logPrintf(pm.logger, "[HTTP-PROXY]", "unknown connection: connID=%d", connID)
		return
	}

	logPrintf(pm.logger, "[HTTP-PROXY]", "received upstream data: connID=%s len=%d", pc.ConnID, len(data))
	_, err := pc.tcpConn.Write(data)
	if err != nil {
		logPrintf(pm.logger, "[HTTP-PROXY]", "write error: connID=%d err=%v", connID, err)
		pm.removeConn(connID)
	}
}

// HandleClose handle proxy_close message
func (pm *ProxyManager) HandleClose(msg *Message) {
	connIDStr := msg.SessionID
	var connID uint32
	fmt.Sscanf(connIDStr, "conn_%08d", &connID)

	logPrintf(pm.logger, "[HTTP-PROXY]", "app requests close: connID=%s", connIDStr)
	pm.removeConn(connID)
}

// removeConn remove and close connection
func (pm *ProxyManager) removeConn(connID uint32) {
	pm.mu.Lock()
	pc, ok := pm.conns[connID]
	if ok {
		delete(pm.conns, connID)
	}
	pm.mu.Unlock()

	if ok {
		pc.once.Do(func() {
			close(pc.cancel)
			pc.tcpConn.Close()
		})
	}
}

// CloseAll close all active proxy connections (called when channel disconnects)
func (pm *ProxyManager) CloseAll() {
	pm.mu.Lock()
	conns := make([]*ProxyConn, 0, len(pm.conns))
	for _, pc := range pm.conns {
		conns = append(conns, pc)
	}
	pm.conns = make(map[uint32]*ProxyConn)
	pm.mu.Unlock()

	for _, pc := range conns {
		pc.once.Do(func() {
			close(pc.cancel)
			pc.tcpConn.Close()
		})
	}
	logPrintf(pm.logger, "[HTTP-PROXY]", "all closed: %d connections total", len(conns))
}
