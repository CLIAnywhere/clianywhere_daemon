#!/bin/bash
set -e
cd "$(dirname "$0")"

# Usage: ./buildweb.sh [platform] [arch]
# platform: linux (default), darwin, windows
# arch:     amd64 (default), arm64, ...
# Example: ./buildweb.sh windows
# Example: ./buildweb.sh darwin arm64

PLATFORM="${1:-linux}"
ARCH="${2:-amd64}"

case "$PLATFORM" in
    linux|darwin)
        GOOS="$PLATFORM"
        ;;
    windows)
        GOOS="windows"
        ;;
    *)
        echo "Usage: $0 [linux|darwin|windows] [amd64|arm64]"
        exit 1
        ;;
esac

echo "Target platform: $GOOS/$ARCH"

# stop running daemon first (only for native builds — both GOOS and GOARCH match host)
HOST_GOOS="$(go env GOOS)"
HOST_GOARCH="$(go env GOARCH)"
if [ "$GOOS" = "$HOST_GOOS" ] && [ "$ARCH" = "$HOST_GOARCH" ]; then
    ./claw-web stop 2>/dev/null || true
fi

# enable local xterm-go replace for build
sed -i 's|^// replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|' go.mod

# restore on exit
trap "sed -i 's|^replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|// replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|' go.mod" EXIT

# build and embed flutter webapp
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [ -f "$SCRIPT_DIR/../localattachwebapp/buildtodaemon.sh" ]; then
    echo "Building localattachwebapp..."
    bash "$SCRIPT_DIR/../localattachwebapp/buildtodaemon.sh"
fi

# Force pure-Go build: cross-compiling to darwin/windows from linux has no C
# toolchain for those targets, and our deps (pion/webrtc, conpty via syscall,
# creack/pty, go-ole) are all pure-Go anyway. CGO_ENABLED=0 also makes the
# binary fully static — easier to ship.
echo "Building claw-web for $GOOS/$ARCH..."
if [ "$GOOS" = "windows" ]; then
    GOOS=$GOOS GOARCH=$ARCH CGO_ENABLED=0 go build -tags web -ldflags "-H windowsgui" -o claw-web.exe ./cmd/claw
    echo "Done: $(pwd)/claw-web.exe"
else
    GOOS=$GOOS GOARCH=$ARCH CGO_ENABLED=0 go build -tags web -o claw-web ./cmd/claw
    echo "Done: $(pwd)/claw-web"
fi
