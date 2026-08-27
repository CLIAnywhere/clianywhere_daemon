package main

// secure_handshake.go — daemon side of the SPAKE2 handshake.
//
// The daemon is the server (B side). It replies to pake_start with pake_reply,
// then waits for sec_confirm. A wrong code surfaces as a key-confirmation tag
// mismatch, which is indistinguishable from network noise except for the single
// sec_code_error response the app uses to update its UI. The
// secCodeAttemptsLeft budget (MaxSecCodeAttempts consecutive failures, daemon
// shuts down at 0) bounds online guessing of the 6-digit code; a successful
// handshake resets the counter.
//
// The security code is loaded once per handshake via LoadSecurityCode(); when
// empty the public default (defaultSecCode) is used so encryption is always on
// (forced encryption). The handshake never transmits the code.

import (
	"encoding/base64"
	"encoding/json"
	"sync/atomic"
	"time"
)

const secureHandshakeTimeout = 10 * time.Second

// secStatusUpdateMsg daemon→TS control message: the security-code state changed
// after login. Marshaled from a struct so "type" stays the first key — the TS
// intercepts it by prefix in its read loop instead of relaying it to the app.
type secStatusUpdateMsg struct {
	Type           string `json:"type"`
	SecCodeEnabled bool   `json:"sec_code_enabled"`
	SecureCap      bool   `json:"secure_cap"`
}

// PushSecStatusUpdate tells the TS relay the security-code state changed
// (set/unset via the local web UI). The TS keeps a login-time snapshot; without
// this push it would keep telling browsers sec_code_enabled=true after the code
// is cleared, making the app prompt for a code on a no-code daemon.
func (d *Daemon) PushSecStatusUpdate() {
	d.wsMu.RLock()
	relay := d.wsRelay
	d.wsMu.RUnlock()
	if relay == nil {
		return
	}
	payload, err := json.Marshal(secStatusUpdateMsg{
		Type:           "sec_status_update",
		SecCodeEnabled: atomic.LoadInt32(&d.secCodeEnabled) == 1,
		SecureCap:      true,
	})
	if err != nil {
		return
	}
	relay.SendRaw(payload)
	d.logger.Infof("[secure] pushed sec_status_update (sec_code_enabled=%v)", atomic.LoadInt32(&d.secCodeEnabled) == 1)
}

// handlePakeStart is the client's first handshake message: {sid, pa}.
// Forced encryption: EVERY connection must complete a SPAKE2 handshake. If the
// daemon has no real security code, the public default (000000) is used as the
// PAKE password (confidentiality only, no auth barrier — matches a no-code
// daemon). The plaintext sec_code_verify path is gone.
func (d *Daemon) handlePakeStart(msg *Message) {
	code := LoadSecurityCode()
	if code == "" {
		code = defaultSecCode
	}

	pa, err := base64.StdEncoding.DecodeString(msg.Pa)
	if err != nil {
		d.logger.Infof("[secure] pake_start: bad pa encoding")
		return
	}
	sid, err := base64.StdEncoding.DecodeString(msg.Sid)
	if err != nil || len(sid) != 16 {
		d.logger.Infof("[secure] pake_start: bad sid")
		return
	}

	w := scalarFromPassword(code)
	server, err := NewSpake2Server(w)
	if err != nil {
		d.logger.Infof("[secure] pake_start: %v", err)
		return
	}
	if err := server.SetPeerShare(pa); err != nil {
		d.logger.Infof("[secure] pake_start: peer share rejected")
		return
	}

	d.secureMu.Lock()
	d.pakeServer = server
	d.pakeSid = sid
	d.pakeAccessKey = d.accessKey
	if d.pakeTimer != nil {
		d.pakeTimer.Stop()
	}
	d.pakeTimer = time.AfterFunc(secureHandshakeTimeout, func() {
		d.secureMu.Lock()
		d.pakeServer = nil
		d.pakeSid = nil
		d.secureMu.Unlock()
		d.logger.Infof("[secure] handshake timed out, reset to plaintext")
	})
	d.secureMu.Unlock()

	d.sendJSON(&Message{
		Type: TypePakeReply,
		Sid:  msg.Sid,
		Pb:   base64.StdEncoding.EncodeToString(server.Share()),
	})
}

// handleSecConfirm verifies the client's key-confirmation tag. On success the
// data plane switches to encrypted; on failure the attempt counter decrements
// (daemon exits at 0) and the state returns to plaintext so the app can retry
// with a fresh handshake.
func (d *Daemon) handleSecConfirm(msg *Message) {
	d.secureMu.Lock()
	server := d.pakeServer
	sid := d.pakeSid
	accessKey := d.pakeAccessKey
	d.secureMu.Unlock()

	if server == nil || sid == nil {
		d.logger.Infof("[secure] sec_confirm without active handshake")
		return
	}

	keys, finishErr := server.Finish(sid, accessKey)
	tt := server.Transcript()
	tag, tagErr := base64.StdEncoding.DecodeString(msg.TagA)

	ok := false
	if finishErr == nil && tagErr == nil && len(tag) == 16 && keys != nil && tt != nil {
		ok = VerifyTag(tag, TagA(keys, tt))
	}

	d.secureMu.Lock()
	if d.pakeTimer != nil {
		d.pakeTimer.Stop()
		d.pakeTimer = nil
	}
	d.pakeServer = nil
	d.pakeSid = nil
	d.secureMu.Unlock()

	if !ok {
		remaining := int32(MaxSecCodeAttempts)
		if atomic.LoadInt32(&d.secCodeEnabled) == 1 {
			// 只有设置了真实安全码才防爆破。无码 daemon 的 PAKE 密码是公开的
			// 000000，任何失败猜测都不该消耗额度（它本就没有认证屏障）。
			remaining = atomic.AddInt32(&d.secCodeAttemptsLeft, -1)
			d.logger.Infof("[secure] sec_confirm rejected, remaining=%d", remaining)
		}
		d.sendJSON(&Message{
			Type:              TypeSecCodeError,
			RemainingAttempts: int(remaining),
		})
		if remaining <= 0 {
			go func() {
				time.Sleep(500 * time.Millisecond)
				d.exitWithReason("sec_code_locked", "")
			}()
		}
		return
	}

	// Success: build the channel. Daemon sends with K_s2c, receives K_c2s.
	sc, err := NewSecureChannel(keys.Ks2c, keys.Kc2s, dirS2C[:], dirC2S[:])
	if err != nil {
		d.logger.Infof("[secure] channel init failed: %v", err)
		return
	}
	d.secureMu.Lock()
	d.secure = sc
	d.secureMu.Unlock()
	atomic.StoreInt32(&d.secCodeVerified, 1)
	// Successful auth proves knowledge of the code: restore the attempt budget so
	// ordinary connections never accumulate failures toward a shutdown (the budget
	// only bounds consecutive brute-force guesses).
	atomic.StoreInt32(&d.secCodeAttemptsLeft, MaxSecCodeAttempts)

	d.sendJSON(&Message{Type: TypeSecOK, TagB: base64.StdEncoding.EncodeToString(TagB(keys, tt))})
	d.sendJSON(&Message{Type: TypeSecureReady})
	d.logger.Infof("[secure] handshake OK, encrypted data plane active")
}

// resetSecure tears down the encrypted channel (keys zeroed) and any pending
// handshake. Called on channel switch, peer offline and transport teardown.
func (d *Daemon) resetSecure(reason string) {
	d.secureMu.Lock()
	if d.secure != nil {
		d.secure.Reset()
		d.secure = nil
	}
	if d.pakeTimer != nil {
		d.pakeTimer.Stop()
		d.pakeTimer = nil
	}
	d.pakeServer = nil
	d.pakeSid = nil
	d.secureMu.Unlock()
	if reason != "" {
		d.logger.Infof("[secure] channel reset (%s)", reason)
	}
}

// secureActive reports whether the encrypted data plane is live.
func (d *Daemon) secureActive() bool {
	d.secureMu.Lock()
	sc := d.secure
	d.secureMu.Unlock()
	return sc != nil && sc.Active()
}

// wrapOutgoingJSON applies the encryption envelope when active. It returns the
// wrapped message, or the original when the channel is not active / the type is
// plaintext-exempt / wrapping failed.
func (d *Daemon) wrapOutgoingJSON(msg *Message) *Message {
	if !d.secureActive() || isPlaintextExempt(msg.Type) {
		return msg
	}
	d.secureMu.Lock()
	sc := d.secure
	d.secureMu.Unlock()
	wrapped, err := sc.WrapJSON(msg)
	if err != nil {
		// Never fall back to plaintext for a message that should be encrypted.
		d.logger.Errorf("[secure] DROP %s: wrap failed: %v", msg.Type, err)
		return nil
	}
	// Diagnostic: trace secure-wrapped dir_list chunks through the send path
	// (paired with the [dirlist] logs in filetransfer.go).
	if msg.Type == TypeDirList {
		if b, err := json.Marshal(msg); err == nil {
			d.logger.Infof("[secure] wrapped dir_list: marshaled=%d bytes", len(b))
		}
	}
	return wrapped
}

// unwrapIncomingJSON decrypts an outer enc_* envelope back to the inner message.
// Returns (inner, true) when the message was an encrypted envelope and was
// successfully decrypted; (nil, false) when it should be dropped.
func (d *Daemon) unwrapIncomingJSON(msg *Message) (*Message, bool) {
	if !isEncryptedType(msg.Type) {
		return msg, true
	}
	if !d.secureActive() {
		d.logger.Infof("[secure] drop enc message while channel inactive")
		return nil, false
	}
	d.secureMu.Lock()
	sc := d.secure
	d.secureMu.Unlock()
	inner, err := sc.UnwrapJSON(msg)
	if err != nil {
		d.logger.Infof("[secure] drop enc message: %v", err)
		return nil, false
	}
	return inner, true
}

// wrapOutgoingBinary applies the binary envelope when active; returns the
// original frame unchanged when not active.
func (d *Daemon) wrapOutgoingBinary(frame []byte) []byte {
	if !d.secureActive() {
		return frame
	}
	d.secureMu.Lock()
	sc := d.secure
	d.secureMu.Unlock()
	return sc.WrapBinary(frame)
}

// unwrapIncomingBinary decrypts a secure binary frame; returns (plain, true) on
// success. When the secure channel is active, plaintext binary is rejected
// (protocol violation).
func (d *Daemon) unwrapIncomingBinary(frame []byte) ([]byte, bool) {
	active := d.secureActive()
	if len(frame) > 0 && frame[0] == OpcodeSecureBinary {
		if !active {
			d.logger.Infof("[secure] drop secure binary while channel inactive")
			return nil, false
		}
		d.secureMu.Lock()
		sc := d.secure
		d.secureMu.Unlock()
		plain, err := sc.UnwrapBinary(frame)
		if err != nil {
			d.logger.Infof("[secure] drop binary: %v", err)
			return nil, false
		}
		return plain, true
	}
	if active {
		d.logger.Infof("[secure] drop plaintext binary while secure active")
		return nil, false
	}
	return frame, true
}

