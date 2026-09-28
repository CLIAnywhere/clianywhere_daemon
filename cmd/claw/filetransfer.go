package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const transFilesDir = ".clianywhere/transfiles"

// File-transfer flow control (relay mode only).
//
// The relay is a single ordered WS stream with no transport-level way to drop
// in-flight data: once the daemon has written chunks into the pipe they keep
// flowing toward the app until consumed. If the app stalls (background freeze,
// slow decrypt), everything already sent piles up in CF's outbound queue and
// the app's stream queue — and terminal text queues behind it (head-of-line
// blocking, the relay twin of the P2P bug in P2P_DATACHANNEL_FIX.md).
//
// Fix: a sliding window. The daemon keeps at most fileAckWindow chunks
// unacknowledged; the app acknowledges every chunk AFTER decrypting it, so the
// window bounds both the CF backlog and the terminal's worst-case wait to one
// window of data. The window matches the app's 4-worker decrypt pool: acks
// fire per decrypted chunk, so fewer in-flight chunks than workers would cap
// decrypt parallelism and halve throughput. P2P mode skips the window — its
// SCTP BufferedAmount backpressure already closes the loop.
const (
	fileAckWindow  = 4                // max unacknowledged chunks in flight (= app decrypt workers; 4×1MB=4MB)
	fileAckTimeout = 30 * time.Second // no ack for this long → abort transfer
)

// fileChunkLimitFor returns the per-chunk frame limit for a file transfer on
// the daemon's current channel. On relay it additionally caps to FileRelayChunk:
// relay forwards frames atomically, so chunk size is the granularity of the
// in-flight data a cancel cannot recall.
func fileChunkLimitFor(d *Daemon) int {
	limit := d.frameLimitForChannel()
	if !d.channelIsP2P() && limit > FileRelayChunk {
		limit = FileRelayChunk
	}
	return limit
}

// StagedFileEntry internal staged file entry (includes local path)
type StagedFileEntry struct {
	Info     StagedFile // protocol struct
	FilePath string     // ~/.clianywhere/transfiles/ full path under
}

// FileTransferManager manage file staging list and send tasks
type FileTransferManager struct {
	mu        sync.Mutex
	active    map[uint32]*FileSendTask // active transfer tasks
	staged    []StagedFileEntry        // staged file list
	daemon    *Daemon
	logger    Logger
	nextID    uint32
	stagedDir string // ~/.clianywhere/transfiles
}

// FileSendTask single file send task
type FileSendTask struct {
	ID          uint32
	FileName    string
	FilePath    string
	FileSize    int64
	TotalChunks int
	SentChunks  int
	StartTime   time.Time
	cancel      chan struct{}

	// Sliding-window flow control (relay mode): app-confirmed chunk count and
	// the wakeup signal for the sender parked on the window.
	acked     atomic.Int32
	ackNotify chan struct{} // cap 1; non-blocking poke from HandleAck

	// cancelOnce guards cancel against a double close. Cancel() and CancelAll()
	// can both fire for the same task (per-file cancel from the app, then
	// CancelAll when P2P drops / browser goes offline), and close() on an
	// already-closed channel panics — which takes the whole daemon down with it,
	// not just the transfer.
	cancelOnce sync.Once
}

// NewFileTransferManager create file transfer manager
func NewFileTransferManager(d *Daemon, logger Logger) *FileTransferManager {
	ftm := &FileTransferManager{
		daemon: d,
		logger: logger,
		active: make(map[uint32]*FileSendTask),
		staged: make([]StagedFileEntry, 0),
	}

	// initialize staging directory, clear old files
	home, err := os.UserHomeDir()
	if err != nil {
		fatalExit("failed to get HOME directory: %v", err)
	}
	ftm.stagedDir = filepath.Join(home, transFilesDir)

	// clear staging directory
	os.RemoveAll(ftm.stagedDir)
	if err := os.MkdirAll(ftm.stagedDir, 0755); err != nil {
		fatalExit("failed to create staging directory %s: %v", ftm.stagedDir, err)
	}

	return ftm
}

// StageFile copy file to staging directory, add to list, notify frontend
func (ftm *FileTransferManager) StageFile(srcPath string) error {
	absPath, err := filepath.Abs(srcPath)
	if err != nil {
		return fmt.Errorf("path resolution failed: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("file does not exist: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("sending directories is not yet supported")
	}

	origName := filepath.Base(absPath)
	ext := filepath.Ext(origName)
	nameWithoutExt := origName[:len(origName)-len(ext)]
	timeStr := time.Now().Format("20060102150405")
	stagedName := nameWithoutExt + "_" + timeStr + ext

	// allocate ID
	fileID := atomic.AddUint32(&ftm.nextID, 1)

	dstPath := filepath.Join(ftm.stagedDir, stagedName)

	// copy file
	src, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open source file failed: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create staged file failed: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("copy file failed: %w", err)
	}

	entry := StagedFileEntry{
		Info: StagedFile{
			ID:           fileID,
			FileName:     stagedName,
			OriginalName: origName,
			Size:         info.Size(),
			Time:         timeStr,
		},
		FilePath: dstPath,
	}

	ftm.mu.Lock()
	ftm.staged = append(ftm.staged, entry)
	ftm.mu.Unlock()

	// notify frontend file list
	ftm.sendFileList()

	return nil
}

// sendFileList send current staged list to frontend
func (ftm *FileTransferManager) sendFileList() {
	ftm.mu.Lock()
	files := make([]StagedFile, 0, len(ftm.staged))
	for _, entry := range ftm.staged {
		files = append(files, entry.Info)
	}
	ftm.mu.Unlock()

	ftm.daemon.sendJSON(&Message{
		Type:  TypeFileList,
		Files: files,
	})
}

// HandleRequest handle frontend request to transfer a file
func (ftm *FileTransferManager) HandleRequest(fileID uint32) error {
	ftm.mu.Lock()
	var entry *StagedFileEntry
	for i, e := range ftm.staged {
		if e.Info.ID == fileID {
			entry = &ftm.staged[i]
			break
		}
	}
	if entry == nil {
		ftm.mu.Unlock()
		return fmt.Errorf("file does not exist (id=%d)", fileID)
	}
	ftm.mu.Unlock()

	// use original send logic
	if err := ftm.startSendFromStaged(entry.FilePath); err != nil {
		return err
	}

	// keep in staged list, only HandleDelete removes it

	return nil
}

// HandleDelete handle frontend delete file request
func (ftm *FileTransferManager) HandleDelete(fileID uint32) {
	ftm.mu.Lock()
	defer ftm.mu.Unlock()

	idx := -1
	for i, e := range ftm.staged {
		if e.Info.ID == fileID {
			idx = i
			break
		}
	}

	if idx >= 0 {
		entry := ftm.staged[idx]
		// delete staged file
		os.Remove(entry.FilePath)
		ftm.staged = append(ftm.staged[:idx], ftm.staged[idx+1:]...)
	}

	// if transferring, also cancel
	if task, ok := ftm.active[fileID]; ok {
		close(task.cancel)
	}
}

// startSendFromStaged start sending file from staging directory
func (ftm *FileTransferManager) startSendFromStaged(filePath string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("file does not exist: %w", err)
	}

	fileSize := info.Size()
	fileName := filepath.Base(filePath)
	// Pick the limit for the current channel: TS relay additionally tightens
	// to FileRelayChunk (1MB — the relay forwards whole frames atomically, so
	// the chunk size is the granularity of what a cancel can no longer recall);
	// the P2P DataChannel only fits 64KB (pion rejects larger with
	// ErrOutboundPacketTooLarge). The chunk size is fixed for the whole
	// transfer and the app reassembles by chunkIdx, so no size negotiation
	// between the ends is required.
	frameLimit := fileChunkLimitFor(ftm.daemon)
	chunkSize := ftm.daemon.cfg.ChunkSize
	secureOverhead := 0
	if ftm.daemon.secureActive() {
		secureOverhead = SecureBinaryOverhead // encrypted binary envelope
	}
	if chunkSize <= 0 || chunkSize > frameLimit-9-secureOverhead {
		chunkSize = frameLimit - 9 - secureOverhead // 9-byte file transfer header
	}
	totalChunks := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))
	if totalChunks == 0 {
		totalChunks = 1
	}

	fileID := atomic.AddUint32(&ftm.nextID, 1)
	task := &FileSendTask{
		ID:          fileID,
		FileName:    fileName,
		FilePath:    filePath,
		FileSize:    fileSize,
		TotalChunks: totalChunks,
		StartTime:   time.Now(),
		cancel:      make(chan struct{}),
		ackNotify:   make(chan struct{}, 1),
	}

	ftm.mu.Lock()
	ftm.active[fileID] = task
	ftm.mu.Unlock()

	ftm.daemon.sendJSON(&Message{
		Type:        TypeFileSendBegin,
		FileID:      fileID,
		FileName:    fileName,
		FileSize:    fileSize,
		TotalChunks: totalChunks,
	})

	go ftm.sendFile(task, chunkSize)

	return nil
}

// StartSend compat with old interface (IPC send command changed to stage)
func (ftm *FileTransferManager) StartSend(filePath string) error {
	return ftm.StageFile(filePath)
}

// ackWaitResult is why an awaitAckWindow wait ended.
type ackWaitResult int

const (
	ackWaitReady ackWaitResult = iota
	ackWaitCancelled
	ackWaitTimeout
)

// awaitAckWindow blocks until the app has acknowledged enough chunks for
// chunkIdx to proceed inside the sliding window (acked + window > chunkIdx),
// the task is cancelled, or no ack arrives within fileAckTimeout (app stalled
// — freeze, network black hole). Pure wait, no side effects.
func (ftm *FileTransferManager) awaitAckWindow(task *FileSendTask, chunkIdx int) ackWaitResult {
	for task.acked.Load()+int32(fileAckWindow) <= int32(chunkIdx) {
		select {
		case <-task.cancel:
			return ackWaitCancelled
		case <-task.ackNotify:
			// re-check acked in the loop condition
		case <-time.After(fileAckTimeout):
			return ackWaitTimeout
		}
	}
	return ackWaitReady
}

// HandleAck records the app's flow-control ack (chunks consumed and decrypted
// so far) and wakes a sender parked on the window.
func (ftm *FileTransferManager) HandleAck(fileID uint32, received int) {
	ftm.mu.Lock()
	task, ok := ftm.active[fileID]
	ftm.mu.Unlock()
	if !ok {
		return
	}
	if int32(received) > task.acked.Load() {
		task.acked.Store(int32(received))
		select {
		case task.ackNotify <- struct{}{}:
		default:
		}
	}
}

// sendFile background read file and send binary chunks
func (ftm *FileTransferManager) sendFile(task *FileSendTask, chunkSize int) {
	defer func() {
		ftm.mu.Lock()
		delete(ftm.active, task.ID)
		ftm.mu.Unlock()
	}()

	f, err := os.Open(task.FilePath)
	if err != nil {
		ftm.daemon.sendJSON(&Message{
			Type:   TypeFileSendError,
			FileID: task.ID,
			Error:  fmt.Sprintf("open file failed: %v", err),
		})
		return
	}
	defer f.Close()

	hash := sha256.New()
	buf := make([]byte, chunkSize)
	header := make([]byte, 9)

	// The sliding window is relay-mode only: the relay has no transport-level
	// way to drop in-flight data, so acks are the only way to bound it; on P2P
	// the SCTP BufferedAmount backpressure already closes the loop.
	useWindow := !ftm.daemon.channelIsP2P()

	for chunkIdx := 0; chunkIdx < task.TotalChunks; chunkIdx++ {
		if useWindow {
			switch ftm.awaitAckWindow(task, chunkIdx) {
			case ackWaitCancelled:
				ftm.daemon.sendJSON(&Message{
					Type:   TypeFileSendCancel,
					FileID: task.ID,
				})
				return
			case ackWaitTimeout:
				ftm.daemon.sendJSON(&Message{
					Type:   TypeFileSendError,
					FileID: task.ID,
					Error:  "ack timeout (app stalled)",
				})
				return
			}
		}
		select {
		case <-task.cancel:
			ftm.daemon.sendJSON(&Message{
				Type:   TypeFileSendCancel,
				FileID: task.ID,
			})
			return
		default:
		}

		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			ftm.daemon.sendJSON(&Message{
				Type:   TypeFileSendError,
				FileID: task.ID,
				Error:  fmt.Sprintf("read failed: %v", err),
			})
			return
		}
		if n == 0 {
			break
		}

		hash.Write(buf[:n])

		header[0] = OpcodeFileTransfer
		binary.BigEndian.PutUint32(header[1:5], task.ID)
		binary.BigEndian.PutUint32(header[5:9], uint32(chunkIdx))

		frame := make([]byte, 9+n)
		copy(frame[0:9], header)
		copy(frame[9:], buf[:n])

		sent := ftm.daemon.sendBytesCancelable(frame, 256*1024, task.cancel)
		if !sent {
			return
		}

		task.SentChunks = chunkIdx + 1

	}
	checksum := fmt.Sprintf("%x", hash.Sum(nil))

	endJSON, _ := json.Marshal(&Message{
		Type:     TypeFileSendEnd,
		FileID:   task.ID,
		Checksum: checksum,
	})
	ctrlFrame := make([]byte, 9+len(endJSON))
	ctrlFrame[0] = OpcodeFileTransfer
	copy(ctrlFrame[9:], endJSON)
	ftm.daemon.sendBytesCancelable(ctrlFrame, 256*1024, task.cancel)
}

// Cancel cancel sending of specific file
// Cancel aborts one in-flight send and removes it from the active map.
//
// Removing it matters: leaving the task behind meant a later CancelAll (P2P drop,
// browser offline, channel switch) would close the same channel again and panic.
func (ftm *FileTransferManager) Cancel(fileID uint32) {
	ftm.mu.Lock()
	task, ok := ftm.active[fileID]
	if ok {
		delete(ftm.active, fileID)
	}
	ftm.mu.Unlock()

	if ok {
		task.cancelOnce.Do(func() { close(task.cancel) })
	}
}

// CancelAll aborts every in-flight send.
func (ftm *FileTransferManager) CancelAll() {
	ftm.mu.Lock()
	tasks := ftm.active
	ftm.active = make(map[uint32]*FileSendTask)
	ftm.mu.Unlock()

	for _, task := range tasks {
		task.cancelOnce.Do(func() { close(task.cancel) })
	}
}

// handleIPCUpload handle HTTP IPC file send request (daemon send command)
func (ftm *FileTransferManager) handleIPCUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	if err := ftm.StageFile(req.Path); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"msg":    "file added to transfer list",
	})
}

// HandleDirListRequest handle directory list request
func (ftm *FileTransferManager) HandleDirListRequest(reqPath string) {
	// A huge-directory listing has crashed the daemon before with no visible
	// log — keep the recover so the process survives, reporting the error to
	// the app instead of exiting.
	defer func() {
		if r := recover(); r != nil {
			ftm.daemon.sendJSON(&Message{
				Type:  TypeDirList,
				Path:  reqPath,
				Error: fmt.Sprintf("dirlist panic: %v", r),
			})
		}
	}()

	targetPath := reqPath
	if targetPath == "" {
		// Windows: show all drive letters
		if runtime.GOOS == "windows" {
			ftm.sendDriveList()
			return
		}
		// non-Windows: default open home
		home, err := os.UserHomeDir()
		if err != nil {
			ftm.daemon.sendJSON(&Message{
				Type:  TypeDirList,
				Path:  "",
				Error: err.Error(),
			})
			return
		}
		targetPath = home
	}

	// frontend uses / as separator uniformly, convert to system separator for local file operations
	targetPath = filepath.Clean(strings.ReplaceAll(targetPath, "/", string(filepath.Separator)))

	// Windows: "C:" is current directory not root, need to append separator
	// Windows: "C:" and "C:." are not root, need to append separator
	// filepath.Clean("C:") returns "C:." on Windows, need to handle both
	if len(targetPath) >= 2 && targetPath[1] == ':' {
		rest := targetPath[2:]
		if rest == "" || rest == "." {
			targetPath = targetPath[:2] + string(filepath.Separator)
		}
	}

	entries, err := os.ReadDir(targetPath)
	if err != nil {
		ftm.daemon.sendJSON(&Message{
			Type:  TypeDirList,
			Path:  toForwardSlash(targetPath),
			Error: err.Error(),
		})
		return
	}

	var dirs, files []DirEntry
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		entry := DirEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().UnixMilli(),
		}
		if e.IsDir() {
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	ftm.sendDirList(toForwardSlash(targetPath), dirs, files)
}

// HandleReqPending handle remote file add-to-pending request
func (ftm *FileTransferManager) HandleReqPending(filePath string) error {
	// frontend uses / separator, convert to system separator
	localPath := filepath.Clean(strings.ReplaceAll(filePath, "/", string(filepath.Separator)))
	return ftm.StageFile(localPath)
}

// toForwardSlash convert system path to / separator (for frontend), ensure drive letter has trailing slash
func toForwardSlash(path string) string {
	p := strings.ReplaceAll(path, "\\", "/")
	if len(p) >= 2 && p[1] == ':' {
		rest := p[2:]
		if rest == "" || rest == "." {
			p = p[:2] + "/"
		}
	}
	return p
}

// sendDriveList list all available drives concurrently, 1s timeout per drive (Windows only)
func (ftm *FileTransferManager) sendDriveList() {
	type result struct {
		name string
		ok   bool
	}
	ch := make(chan result, 26)

	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		go func(l rune) {
			drivePath := string(l) + ":" + string(filepath.Separator)
			ok := false
			done := make(chan struct{})
			go func() {
				fi, err := os.Stat(drivePath)
				ok = err == nil && fi.IsDir()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			ch <- result{name: string(l) + ":", ok: ok}
		}(letter)
	}

	var dirs []DirEntry
	for i := 0; i < 26; i++ {
		r := <-ch
		if r.ok {
			dirs = append(dirs, DirEntry{Name: r.name, Size: 0})
		}
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	ftm.sendDirList("", dirs, []DirEntry{})
}

// dirListEnvelopeOverhead is the safety margin (bytes) reserved for the
// dir_list message envelope ("type","path","seq","total_chunks", etc.) on
// top of the summed entry sizes when packing chunks.
const dirListEnvelopeOverhead = 1000

// sendDirList sends a directory listing as one or more dir_list messages.
// A huge directory can exceed SecureMaxTextPayload (24KB inner JSON limit
// enforced by the E2E secure channel) in a single message, so entries are
// packed into chunks sized by their actual marshaled length — the same
// chunking strategy as terminal history, but reusing the Message
// TotalChunks/ChunkIndex fields on dir_list itself. Entries are sent in
// order (all dirs first, then all files) so the app can simply concatenate
// chunks in arrival order.
func (ftm *FileTransferManager) sendDirList(path string, dirs, files []DirEntry) {
	// exact marshaled size of each entry, in final send order (dirs then files)
	sizes := make([]int, 0, len(dirs)+len(files))
	for i := range dirs {
		b, _ := json.Marshal(dirs[i])
		sizes = append(sizes, len(b)+1) // +1 for the JSON comma separator
	}
	for i := range files {
		b, _ := json.Marshal(files[i])
		sizes = append(sizes, len(b)+1)
	}

	budget := int(SecureMaxTextPayload) - dirListEnvelopeOverhead

	// pack entry index ranges [start,end) into chunks within budget
	type span struct{ start, end int }
	var chunks []span
	start, cost := 0, 0
	for i, s := range sizes {
		if cost > 0 && cost+s > budget {
			chunks = append(chunks, span{start, i})
			start, cost = i, 0
		}
		cost += s
	}
	chunks = append(chunks, span{start, len(sizes)})

	ndirs := len(dirs)
	for i, c := range chunks {
		// map the combined [start,end) range back to dirs/files sub-slices.
		// dLo must clamp to ndirs too: a chunk starting past the dirs/files
		// boundary (e.g. dirs=1, chunk starts at 306) would otherwise index
		// dirs[306:1] and panic.
		dLo, dHi := min(c.start, ndirs), min(c.end, ndirs)
		fLo, fHi := max(c.start-ndirs, 0), max(c.end-ndirs, 0)
		msg := &Message{
			Type:        TypeDirList,
			Path:        path,
			TotalChunks: len(chunks),
			ChunkIndex:  i,
			Entries: &DirEntries{
				Dirs:  dirs[dLo:dHi],
				Files: files[fLo:fHi],
			},
		}
		ftm.daemon.sendJSONWithBackpressure(msg, 64*1024)
	}
}
