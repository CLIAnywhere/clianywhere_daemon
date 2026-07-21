package main

// MaxFrameSize is the maximum allowed WebSocket frame size in bytes (both text and binary).
// DO NOT increase this value — the TurnServer will disconnect any connection
// that sends a frame larger than 32KB. Keep all payloads (terminal output,
// file transfer, proxy tunnel, etc.) within this limit including their headers.
const MaxFrameSize = 31 * 1024 // 31KB

// Binary frame Opcodes (first byte)
const (
	OpcodeFileTransfer = 0x01 // file transfer: [0x01][4B fileId][4B chunkIdx][data...] (9-byte header)
	// Reserved: 0x02 image, 0x03 clipboard, 0x04 port forwarding, etc.
	OpcodeProxyData = 0x05 // proxy tunnel data: [0x05][4B connId BE][payload...] (5-byte header)
)

// Message type constants — must match daemon_node/protocol.js and frontend terminal.html
const (
	TypeAuth           = "auth"
	TypeAuthOK         = "auth_ok"
	TypeAuthError      = "auth_error"
	TypePing           = "ping"
	TypePong           = "pong"
	TypeCreateSession  = "create_session"
	TypeSessionCreated = "session_created"
	TypeDestroySession = "destroy_session"
	TypeSessionExited  = "session_exit"
	TypeInput          = "input"
	TypeOutput         = "output"
	TypeResize         = "resize"
	TypeError          = "error"
	TypeSessionList    = "session_list"
	TypeSessionInfo    = "session_info"
	TypeRequestHistory  = "request_history"
	TypeHistoryData     = "history_data"
	TypeHistoryStart    = "history_start"
	TypeHistoryChunk    = "history_chunk"
	TypeHistoryEnd      = "history_end"
	TypeChannelSelect   = "channel_select"
	TypeChannelSelected = "channel_selected"
	TypeChannelFailed   = "channel_failed"
	TypeKicked          = "kicked"
	TypePeerOffline     = "peer_offline"

	// file transfer
	TypeFileSendBegin = "file_send_begin"
	TypeFileSendEnd   = "file_send_end"
	TypeFileSendCancel = "file_send_cancel"
	TypeFileSendError = "file_send_error"
	TypeFileList      = "file_list"       // Daemon→App: staged file list
	TypeFileListRequest = "file_list_request" // App->Daemon: request staged file list
	TypeFileRequest   = "file_request"    // App→Daemon: request file transfer
	TypeFileDelete    = "file_delete"     // App

		// Remote file browsing
		TypeDirListRequest = "dir_list_request" // App→Daemon: request directory listing
		TypeDirList        = "dir_list"         // Daemon→App: return directory content
		TypeReqPending     = "req_pending"      // App→Daemon: request add to pending

	// HTTP proxy tunnel
	TypeProxyConnect   = "proxy_connect"   // App→Daemon: request TCP connection
	TypeProxyConnected = "proxy_connected" // Daemon→App: connection successful
	TypeProxyError     = "proxy_error"     // Daemon→App: connection failed
	TypeProxyClose     = "proxy_close"     // Bidirectional: close notification

	// HTTP proxy fetch (Web app: SW-based proxy)
	TypeProxyHttpFetch    = "proxy_http_fetch"    // App→Daemon: HTTP-level fetch request
	TypeProxyHttpResponse = "proxy_http_response" // Daemon→App: HTTP fetch response

	// WebSocket proxy (Web app: SW-based proxy)
	TypeProxyWsConnect   = "proxy_ws_connect"   // App→Daemon: request WS connection
	TypeProxyWsConnected = "proxy_ws_connected" // Daemon→App: WS connection established
	TypeProxyWsMessage   = "proxy_ws_message"   // Bidirectional: WS data frame
	TypeProxyWsClose     = "proxy_ws_close"     // Bidirectional: WS close
	TypeProxyWsError     = "proxy_ws_error"     // Daemon→App: WS connection error

	// Local Attach
	TypeAttach        = "attach"         // Client→LocalServer: request attach session
	TypeDetach        = "detach"         // Client→LocalServer: detach session
	TypeAttachOK      = "attach_ok"      // LocalServer→Client: attach success confirmation
	TypeLocalTakeover = "local_takeover" // Client→LocalServer: local takeover, kick app
	TypeStatus        = "status"         // Client→LocalServer: query daemon connection status
	TypeStop          = "stop"           // Client→LocalServer: stop daemon process

	// Security Code (remote connection verification)
	TypeSecCodeVerify      = "sec_code_verify"       // App→Daemon: verify security code
	TypeSecCodeOK          = "sec_code_ok"           // Daemon→App: verification passed
	TypeSecCodeError       = "sec_code_error"        // Daemon→App: verification failed (also used for unauthorized operations)

	// Security Code Management (local connection)
	TypeSetSecCode         = "set_sec_code"          // LocalWeb/CLI→Daemon: set security code
	TypeSetSecCodeResult   = "set_sec_code_result"   // Daemon→Local: set result
	TypeUnsetSecCode       = "unset_sec_code"        // LocalWeb/CLI→Daemon: clear security code
	TypeUnsetSecCodeResult = "unset_sec_code_result" // Daemon→Local: clear result
	TypeGetSecCodeStatus   = "get_sec_code_status"   // LocalWeb→Daemon: query security code state
	TypeSecCodeStatus      = "sec_code_status"       // Daemon→Local: return whether code is set

	// Server Manager (Local Attach extension for web UI)
	TypeGetServerStatus   = "get_server_status"   // Client→LocalServer: query daemon state + accesskey
	TypeServerStatus      = "server_status"        // LocalServer→Client: return state + masked accesskey
	TypeSetAccessKey      = "set_accesskey"        // Client→LocalServer: set accesskey and start remote
	TypeAccessKeyResult   = "accesskey_result"     // LocalServer→Client: set accesskey result
	TypeRequestBindCode   = "request_bindcode"     // Client→LocalServer: request QR binding flow
	TypeBindCodeResult    = "bindcode_result"      // LocalServer→Client: return QR payload
	TypeBindCodeAccessKey = "bindcode_accesskey"   // LocalServer→Client: push accesskey from QR scan
	TypeConfirmBindCode   = "confirm_bindcode"     // Client→LocalServer: confirm and save QR accesskey
	TypeSubscribeLogs     = "subscribe_logs"       // Client→LocalServer: subscribe to log stream
	TypeUnsubscribeLogs   = "unsubscribe_logs"     // Client→LocalServer: unsubscribe from log stream
	TypeLogData           = "log_data"             // LocalServer→Client: log entry push

	// Desktop Shortcut (Windows localweb only — first browser connection)
	TypeAskShortcut      = "ask_shortcut"      // Daemon→App: ask user whether to create desktop shortcut
	TypeShortcutResponse = "shortcut_response" // App→Daemon: user's choice (Success=true means yes)
	TypeShortcutResult   = "shortcut_result"   // Daemon→App: result of createDesktopShortcut (Success + Error)
)

// DataChannel JSON message envelope
type Message struct {
	Type         string                   `json:"type"`
	Reason       string                   `json:"reason,omitempty"`
	SessionID    string                   `json:"session_id,omitempty"`
	Data         string                   `json:"data,omitempty"`
	Shell        string                   `json:"shell,omitempty"`
	LoginShell   *bool                    `json:"login_shell,omitempty"` // nil → use default (true)
	Cols         int                      `json:"cols,omitempty"`
	Rows         int                      `json:"rows,omitempty"`
	PID          int                      `json:"pid,omitempty"`
	ExitCode     int                      `json:"exit_code,omitempty"`
	Error        string                   `json:"error,omitempty"`
	Sessions     []string                 `json:"sessions,omitempty"`
	SessionInfos []SessionInfo            `json:"session_infos,omitempty"`
	Shells       []ShellInfo              `json:"shells,omitempty"`
	Seq          uint64                   `json:"seq,omitempty"`
	TotalChunks  int                      `json:"total_chunks"`
	ChunkIndex   int                      `json:"chunk_index"`
	Extra        map[string]any `json:"-"`

	// file transfer
	FileID         uint32 `json:"file_id,omitempty"`
	FileName       string `json:"file_name,omitempty"`
	FileSize       int64  `json:"file_size,omitempty"`
	Checksum       string `json:"checksum,omitempty"`
	OriginalName   string `json:"original_name,omitempty"`
	FileTime       string `json:"file_time,omitempty"`
	Files          []StagedFile `json:"files,omitempty"`
	Path           string       `json:"path,omitempty"`
	Entries        *DirEntries  `json:"entries,omitempty"`
	Name           string       `json:"name,omitempty"`
	SystemInfo     string       `json:"system_info,omitempty"`
	SecCodeRequired bool        `json:"sec_code_required,omitempty"`

	// server manager
	AccessKey  string     `json:"accesskey,omitempty"`
	DeviceName string     `json:"device_name,omitempty"`
	QRPayload  string     `json:"qr_payload,omitempty"`
	BindCode   string     `json:"bindcode,omitempty"`
	Success    bool       `json:"success,omitempty"`
	LogEntries []LogEntry `json:"log_entries,omitempty"`

	// HTTP proxy fetch (Web SW proxy)
	StatusCode  int    `json:"status_code,omitempty"`
	StatusText  string `json:"status_text,omitempty"`
	HeadersJSON string `json:"headers_json,omitempty"`
	Method      string `json:"method,omitempty"`
	BodyBase64  string `json:"body_base64,omitempty"`

	// WebSocket proxy
	IsBinary bool `json:"is_binary,omitempty"` // WS message is binary (vs text)
}

// SessionInfo session summary information
type SessionInfo struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	Shell     string `json:"shell"`
	CreatedAt int64  `json:"created_at"`
	Name      string `json:"name"`
}

// ShellInfo available shell entry
type ShellInfo struct {
	Name string `json:"name"` // display name: "bash", "zsh", "cmd", "pwsh", etc.
	Path string `json:"path"` // absolute path: "/bin/bash", resolved at runtime on Windows
}

// StagedFile staged file entry for transfer
type StagedFile struct {
	ID           uint32 `json:"id"`
	FileName     string `json:"file_name"`     // filename with timestamp
	OriginalName string `json:"original_name"` // original filename
	Size         int64  `json:"size"`
	Time         string `json:"time"` // format: yyyyMMddHHmmss
}

// DirEntry directory entry (file or subdirectory)
type DirEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"` // modification time, unix milliseconds
}

// DirEntries directory content (dirs first, then files)
type DirEntries struct {
	Dirs  []DirEntry `json:"dirs"`
	Files []DirEntry `json:"files"`
}
