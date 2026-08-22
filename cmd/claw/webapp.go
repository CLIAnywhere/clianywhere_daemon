//go:build web

package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed localattachwebapp.zip
var webappZip []byte

// readExternalWebappZip loads the web app zip from the .app bundle's Resources
// directory when running from a packaged CLIAnywhere.app (macOS builds keep the
// zip there as a standalone file so a future universal binary does not carry a
// duplicate copy per architecture). Returns nil when not applicable so the
// embedded zip is used as a fallback.
func readExternalWebappZip() []byte {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	const marker = ".app" + string(filepath.Separator) + "Contents" + string(filepath.Separator) + "MacOS"
	if i := strings.LastIndex(exe, marker); i >= 0 {
		appRoot := exe[:i+len(".app")]
		p := filepath.Join(appRoot, "Contents", "Resources", "localattachwebapp.zip")
		if data, err := os.ReadFile(p); err == nil {
			return data
		}
	}
	return nil
}

// webappFiles loads files from the embedded zip; key is "/filepath", value is the file content.
var webappFiles map[string][]byte

// serviceWorkerSettings pattern used to strip the block from flutter_bootstrap.js.
var svcWorkerRe = regexp.MustCompile(`serviceWorkerSettings:\s*\{[^}]*\},?\s*`)

// flutter_bootstrap.js ends with its own _flutter.loader.load({...}) call,
// while index.html also calls load() from script.onload.
// Two load() calls = two <script src=main.dart.js> = two JS contexts.
// We strip the bootstrap's own call here and keep only the one in the HTML.
var bootstrapLoadRe = regexp.MustCompile(`_flutter\.loader\.load\(\s*\{[^}]*\}\s*\)\s*;?`)

func init() {
	webappFiles = make(map[string][]byte)
	// Packaged macOS .app loads the zip from Contents/Resources; everything
	// else (Linux/Windows and bare binaries) uses the embedded copy.
	zipData := webappZip
	if external := readExternalWebappZip(); external != nil {
		zipData = external
	}
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		// zip missing or corrupt: webappFiles stays empty; serveWebApp returns 404.
		return
	}
	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		// Inside the zip the path may be "index.html" or "./index.html"; normalize to "/index.html".
		name := f.Name
		name = strings.TrimPrefix(name, "./")
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		webappFiles[name] = data
	}
}

// startWebAppServer starts a local HTTP server and returns (port, error).
// It scans ports starting from startPort and tries at most `count` of them.
func startWebAppServer(startPort, count int) (int, error) {
	for port := startPort; port < startPort+count; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/", serveWebApp)
		server := &http.Server{Handler: mux}
		go server.Serve(listener)
		return port, nil
	}
	return -1, fmt.Errorf("no available port in range %d-%d", startPort, startPort+count-1)
}

// serveWebApp reads a file from the in-memory zip and writes the HTTP response.
func serveWebApp(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	data, ok := webappFiles[path]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Version-stamp flutter_bootstrap.js in index.html so it isn't cached,
	// and version-stamp main.dart.js inside flutter_bootstrap.js so it isn't cached either.
	// This forms a full cache-busting chain: index.html -> flutter_bootstrap.js?v=X -> main.dart.js?v=Y.
	if path == "/index.html" || path == "/flutter_bootstrap.js" || path == "/flutter.js" {
		version := strconv.FormatInt(time.Now().UnixNano(), 36)
		if path == "/index.html" {
			// Stamp flutter_bootstrap.js with a version to bypass cache.
			data = bytes.Replace(data,
				[]byte(`src="flutter_bootstrap.js"`),
				[]byte(`src="flutter_bootstrap.js?v=`+version+`"`),
				1)
		} else {
			// Strip the serviceWorkerSettings block to fully disable the service worker.
			data = svcWorkerRe.ReplaceAll(data, []byte{})
			// Strip the bootstrap's own _flutter.loader.load() call to avoid
			// creating a second Flutter instance alongside the one from index.html.
			data = bootstrapLoadRe.ReplaceAll(data, []byte{})
		}
		// flutter_bootstrap.js / flutter.js reference "main.dart.js" in several places
		// (default loadEntrypoint arg, load fallback, "mainJsPath" config, etc.).
		// Replace all of them so the actually used one is never missed.
		data = bytes.Replace(data,
			[]byte(`"main.dart.js"`),
			[]byte(`"main.dart.js?v=`+version+`"`),
			-1)
	}

	// Set Content-Type based on the file extension.
	ext := filepath.Ext(path)
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	// Disable browser caching so every load fetches the latest version.
	// A stale main.dart.js kept alive by the service worker would be incompatible with newer daemons.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Write(data)
}
