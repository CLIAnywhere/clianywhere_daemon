package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeProxy is a minimal HTTP CONNECT proxy for tests. It accepts one
// connection, captures the request line and headers, replies with status, and
// then echoes everything back (prefixed) so the tunnel can be verified.
type fakeProxy struct {
	addr        string
	requestLine chan string
	headers     chan string
}

func startFakeProxy(t *testing.T, status string) *fakeProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fp := &fakeProxy{
		addr:        ln.Addr().String(),
		requestLine: make(chan string, 1),
		headers:     make(chan string, 1),
	}
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		fp.requestLine <- strings.TrimRight(line, "\r\n")
		var hdrs strings.Builder
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if l == "\r\n" || l == "\n" {
				break
			}
			hdrs.WriteString(l)
		}
		fp.headers <- hdrs.String()
		io.WriteString(c, "HTTP/1.1 "+status+"\r\n\r\n")
		if !strings.HasPrefix(status, "200") {
			return
		}
		buf := make([]byte, 64)
		n, err := br.Read(buf)
		if err != nil {
			return
		}
		c.Write(append([]byte("ECHO:"), buf[:n]...))
	}()
	t.Cleanup(func() { ln.Close() })
	return fp
}

func (fp *fakeProxy) url() *url.URL {
	u, _ := url.Parse("http://" + fp.addr)
	return u
}

func TestDialThroughProxyKeepsHostname(t *testing.T) {
	fp := startFakeProxy(t, "200 Connection established")

	// The target host name does not resolve locally on purpose: the dialer
	// must hand it to the proxy verbatim and never resolve it itself.
	conn, err := dialThroughProxy(context.Background(), fp.url(), "ts.example.invalid:443")
	if err != nil {
		t.Fatalf("dialThroughProxy: %v", err)
	}
	defer conn.Close()

	if got := <-fp.requestLine; got != "CONNECT ts.example.invalid:443 HTTP/1.1" {
		t.Fatalf("CONNECT request line = %q, want host name passed through unresolved", got)
	}

	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if string(buf[:n]) != "ECHO:ping" {
		t.Fatalf("tunnel echo = %q", string(buf[:n]))
	}
}

func TestDialThroughProxySendsBasicAuth(t *testing.T) {
	fp := startFakeProxy(t, "200 Connection established")
	u := fp.url()
	u.User = url.UserPassword("hatch", "s3cret")

	conn, err := dialThroughProxy(context.Background(), u, "ts.clianywhere.com:443")
	if err != nil {
		t.Fatalf("dialThroughProxy: %v", err)
	}
	defer conn.Close()

	headers := <-fp.headers
	// base64("hatch:s3cret")
	if !strings.Contains(headers, "Proxy-Authorization: Basic aGF0Y2g6czNjcmV0") {
		t.Fatalf("missing/wrong Proxy-Authorization header: %q", headers)
	}
}

func TestDialThroughProxy407(t *testing.T) {
	fp := startFakeProxy(t, "407 Proxy Authentication Required")
	_, err := dialThroughProxy(context.Background(), fp.url(), "ts.clianywhere.com:443")
	if err == nil {
		t.Fatal("expected error on 407")
	}
	if !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("error should mention authentication: %v", err)
	}
}

func TestParseProxyFlag(t *testing.T) {
	t.Cleanup(func() { _ = SetOutboundProxy("off") })

	rest, err := parseProxyFlag([]string{"--proxy", "http://198.19.0.1:3128", "send", "a.txt"})
	if err != nil {
		t.Fatalf("parseProxyFlag: %v", err)
	}
	if strings.Join(rest, " ") != "send a.txt" {
		t.Fatalf("remaining args = %v", rest)
	}
	p := currentProxy()
	if p == nil || p.Host != "198.19.0.1:3128" {
		t.Fatalf("installed proxy = %v", p)
	}

	// --proxy=value form
	if _, err := parseProxyFlag([]string{"--proxy=http://hatch-egress-proxy:3128"}); err != nil {
		t.Fatalf("equals form: %v", err)
	}

	// off restores direct mode
	if _, err := parseProxyFlag([]string{"--proxy", "off"}); err != nil {
		t.Fatalf("off: %v", err)
	}
	if ProxyEnabled() {
		t.Fatal("proxy should be disabled after off")
	}

	// missing value / unsupported scheme / auto must all fail loudly
	for _, bad := range [][]string{
		{"--proxy"},
		{"--proxy", "socks5://1.2.3.4:1080"},
		{"--proxy", "auto"},
		{"--proxy", ""},
	} {
		if _, err := parseProxyFlag(bad); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}

func TestShouldBypassProxy(t *testing.T) {
	cases := []struct {
		addr          string
		bypassPrivate bool
		want          bool
	}{
		{"127.0.0.1:8787", false, true},         // loopback always direct
		{"localhost:8787", false, true},         // loopback always direct
		{"[::1]:8787", false, true},             // loopback always direct
		{"10.0.0.5:80", false, false},           // relay targets go to the proxy
		{"10.0.0.5:80", true, true},             // app targets on LAN stay local
		{"192.168.1.9:22", true, true},          // app targets on LAN stay local
		{"ts.clianywhere.com:443", true, false}, // names are proxy-resolved
		{"198.19.0.1:3128", false, false},       // proxy host itself handled elsewhere
	}
	for _, c := range cases {
		if got := shouldBypassProxy(c.addr, c.bypassPrivate); got != c.want {
			t.Errorf("shouldBypassProxy(%q, %v) = %v, want %v", c.addr, c.bypassPrivate, got, c.want)
		}
	}
}

// startForwardingProxy runs a real HTTP CONNECT proxy that pipes the tunnel to
// dialTarget regardless of the requested host:port, so tests can present a
// made-up target name and still reach a local server.
func startForwardingProxy(t *testing.T, dialTarget string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				if _, err := br.ReadString('\n'); err != nil {
					return
				}
				for {
					l, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if l == "\r\n" {
						break
					}
				}
				up, err := net.Dial("tcp", dialTarget)
				if err != nil {
					io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer up.Close()
				io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() { io.Copy(up, br) }()
				io.Copy(c, up)
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// selfSignedCert makes a throwaway certificate for wantHost.
func selfSignedCert(t *testing.T, wantHost string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: wantHost},
		DNSNames:     []string{wantHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestWebSocketTLSThroughProxy is the acceptance-shaped test: a wss://
// handshake to a target host name completes THROUGH the CONNECT proxy, with
// the target name used as SNI (proving TLS is end-to-end over the tunnel and
// the name was never resolved locally).
func TestWebSocketTLSThroughProxy(t *testing.T) {
	t.Cleanup(func() { _ = SetOutboundProxy("off") })

	const wantHost = "ts.example.invalid"
	cert := selfSignedCert(t, wantHost)
	sniCh := make(chan string, 1)
	tlsLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			select {
			case sniCh <- chi.ServerName:
			default:
			}
			return &cert, nil
		},
	})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	t.Cleanup(func() { tlsLn.Close() })

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, err := upgrader.Upgrade(w, r, nil); err == nil {
			conn.Close()
		}
	})}
	go srv.Serve(tlsLn)
	t.Cleanup(func() { srv.Close() })

	proxyAddr := startForwardingProxy(t, tlsLn.Addr().String())
	if err := SetOutboundProxy("http://" + proxyAddr); err != nil {
		t.Fatalf("SetOutboundProxy: %v", err)
	}

	_, port, _ := net.SplitHostPort(tlsLn.Addr().String())
	wssURL := "wss://" + net.JoinHostPort(wantHost, port) + "/ws"
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		NetDialContext:   dialOutbound,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, // test cert
	}
	conn, _, err := dialer.Dial(wssURL, nil)
	if err != nil {
		t.Fatalf("wss dial through proxy: %v", err)
	}
	conn.Close()

	select {
	case sni := <-sniCh:
		if sni != wantHost {
			t.Fatalf("SNI = %q, want %q", sni, wantHost)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no TLS ClientHello seen by server")
	}
}
