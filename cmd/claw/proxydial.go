package main

// HTTP CONNECT proxy support for daemon outbound connections.
//
// The --proxy flag routes every outbound TCP connection (relay WSS, bind WSS,
// update checks, and the app-driven tunnels) through an HTTP CONNECT proxy.
// Without the flag the behavior is byte-for-byte the old direct dialing.
//
// Remote DNS: the CONNECT request carries the target host name verbatim and
// the proxy resolves it. We never resolve target names locally — the managed
// VM's DNS is fake-IP mode (198.18.0.0/15 answers), so a locally-resolved IP
// is unreachable from the proxy side. The proxy address itself IS resolved
// locally, on every dial (no cache), because it lives on a platform-internal
// network that only the local resolver knows.

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CLIAnywhere/clianywhere_daemon/internal/checkupdate"
)

const (
	// proxyConnectTimeout bounds dial + CONNECT handshake to the proxy.
	proxyConnectTimeout = 10 * time.Second
	// proxyHeaderLimit caps the CONNECT response header size we are willing
	// to read before giving up on a misbehaving proxy.
	proxyHeaderLimit = 64 * 1024
)

var (
	outboundProxyMu sync.RWMutex
	outboundProxy   *url.URL // nil = direct connection
)

// parseProxyFlag scans args for --proxy <value> / --proxy=<value>, installs the
// value, and returns the remaining args. The flag is left in os.Args on purpose:
// forkToBackground re-executes this binary with os.Args[1:] and the child
// process parses the flag again itself.
func parseProxyFlag(args []string) ([]string, error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var spec string
		found := false
		switch {
		case arg == "--proxy":
			if i+1 >= len(args) {
				return nil, errors.New("--proxy requires a value (http://host:port)")
			}
			spec = args[i+1]
			i++
			found = true
		case strings.HasPrefix(arg, "--proxy="):
			spec = strings.TrimPrefix(arg, "--proxy=")
			found = true
		}
		if !found {
			rest = append(rest, arg)
			continue
		}
		if strings.TrimSpace(spec) == "" {
			return nil, errors.New("--proxy requires a value (http://host:port, or off)")
		}
		if err := SetOutboundProxy(spec); err != nil {
			return nil, err
		}
	}
	return rest, nil
}

// SetOutboundProxy installs the proxy from a --proxy value.
// "" or "off" disables the proxy (direct). Only http:// proxies are accepted.
func SetOutboundProxy(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "off") {
		outboundProxyMu.Lock()
		outboundProxy = nil
		outboundProxyMu.Unlock()
		checkupdate.SetProxyDialContext(nil)
		return nil
	}

	u, err := url.Parse(spec)
	if err != nil {
		return fmt.Errorf("invalid --proxy %q: %w", spec, err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("invalid --proxy %q: only http:// proxies are supported", spec)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("invalid --proxy %q: missing host", spec)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "80")
	}

	outboundProxyMu.Lock()
	outboundProxy = u
	outboundProxyMu.Unlock()

	// Let the update checker use the same egress path; otherwise `claw update`
	// is dead on a host whose direct egress is blocked.
	checkupdate.SetProxyDialContext(dialOutbound)

	writeEarlyLog(fmt.Sprintf("[PROXY] outbound via %s", proxyDisplay(u)))
	return nil
}

// ProxyEnabled reports whether outbound connections must use the proxy.
func ProxyEnabled() bool {
	return currentProxy() != nil
}

func currentProxy() *url.URL {
	outboundProxyMu.RLock()
	defer outboundProxyMu.RUnlock()
	return outboundProxy
}

// proxyDisplay is the loggable form: scheme://host:port without credentials.
func proxyDisplay(p *url.URL) string {
	return p.Scheme + "://" + p.Host
}

// dialOutbound dials for daemon-owned connections (relay WSS, bind WSS,
// update checks): always through the proxy when one is configured.
func dialOutbound(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialMaybeProxy(ctx, network, addr, false)
}

// dialAppTarget dials targets requested by the app (proxy tunnels, HTTP fetch).
// Loopback targets bypass the proxy: they belong to the daemon's own machine,
// not to the egress path, and a CONNECT to 127.0.0.1 would hit the proxy host
// instead. LAN/private targets bypass too for the same reason.
func dialAppTarget(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialMaybeProxy(ctx, network, addr, true)
}

func dialMaybeProxy(ctx context.Context, network, addr string, bypassPrivate bool) (net.Conn, error) {
	p := currentProxy()
	if p == nil || !strings.HasPrefix(network, "tcp") {
		return directDial(ctx, network, addr)
	}
	if shouldBypassProxy(addr, bypassPrivate) {
		return directDial(ctx, network, addr)
	}
	return dialThroughProxy(ctx, p, addr)
}

// directDial is the plain net.Dial path, keeping the TCP_NODELAY behavior the
// previous per-call dialers set explicitly.
func directDial(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	return c, nil
}

// shouldBypassProxy decides without resolving anything: loopback and
// localhost always bypass; private/link-local IP literals bypass only when the
// caller asked for it (app-driven targets). Domain names never bypass here —
// they go to the proxy, which resolves them remotely.
func shouldBypassProxy(addr string, bypassPrivate bool) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	if bypassPrivate && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return true
	}
	return false
}

// dialThroughProxy opens a TCP connection to the proxy and issues
// "CONNECT <addr>". addr is passed through untouched, so the proxy performs
// DNS resolution for the target.
func dialThroughProxy(ctx context.Context, p *url.URL, addr string) (net.Conn, error) {
	proxyAddr := p.Host
	if p.Port() == "" {
		proxyAddr = net.JoinHostPort(p.Hostname(), "80")
	}

	// Resolved on every dial on purpose: the proxy's IP may change on platform
	// reconfiguration; only its host name is stable. The dial itself is bounded
	// here as well — the SetDeadline below starts only after it returns.
	c, err := (&net.Dialer{Timeout: proxyConnectTimeout}).DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy %s dial failed: %w", proxyAddr, err)
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	deadline := time.Now().Add(proxyConnectTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)

	var req strings.Builder
	req.WriteString("CONNECT ")
	req.WriteString(addr)
	req.WriteString(" HTTP/1.1\r\nHost: ")
	req.WriteString(addr)
	req.WriteString("\r\n")
	if p.User != nil {
		pass, _ := p.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(p.User.Username() + ":" + pass))
		req.WriteString("Proxy-Authorization: Basic ")
		req.WriteString(cred)
		req.WriteString("\r\n")
	}
	req.WriteString("\r\n")

	if _, err := io.WriteString(c, req.String()); err != nil {
		c.Close()
		return nil, fmt.Errorf("proxy %s CONNECT %s: write failed: %w", proxyAddr, addr, err)
	}

	br := bufio.NewReader(c)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("proxy %s CONNECT %s: read response: %w", proxyAddr, addr, err)
	}
	statusLine = strings.TrimRight(statusLine, "\r\n")
	parts := strings.SplitN(statusLine, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		c.Close()
		return nil, fmt.Errorf("proxy %s CONNECT %s: malformed response %q", proxyAddr, addr, statusLine)
	}
	code, convErr := strconv.Atoi(parts[1])
	if convErr != nil {
		c.Close()
		return nil, fmt.Errorf("proxy %s CONNECT %s: malformed status %q", proxyAddr, addr, statusLine)
	}

	// Drain (and discard) the response headers. Bytes buffered by the proxy
	// beyond the header block are not lost: the returned conn reads through br.
	total := 0
	for {
		line, err := br.ReadString('\n')
		total += len(line)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("proxy %s CONNECT %s: read headers: %w", proxyAddr, addr, err)
		}
		if total > proxyHeaderLimit {
			c.Close()
			return nil, fmt.Errorf("proxy %s CONNECT %s: response headers exceed %d bytes", proxyAddr, addr, proxyHeaderLimit)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	if code != 200 {
		c.Close()
		reason := ""
		if len(parts) == 3 {
			reason = parts[2]
		}
		if code == 407 {
			return nil, fmt.Errorf("proxy %s CONNECT %s: HTTP 407 authentication required (add user:pass@ to the --proxy URL)", proxyAddr, addr)
		}
		return nil, fmt.Errorf("proxy %s CONNECT %s rejected: HTTP %d %s", proxyAddr, addr, code, reason)
	}

	_ = c.SetDeadline(time.Time{})
	return &bufferedConn{Conn: c, r: br}, nil
}

// bufferedConn preserves bytes the proxy may have already sent together with
// the CONNECT response headers.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.r.Read(p)
}
