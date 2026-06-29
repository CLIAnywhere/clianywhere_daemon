#!/bin/bash
set -e
cd "$(dirname "$0")"

# Usage: ./buildcli.sh [platform]
# platform: linux (default), darwin, windows
# Example: ./buildcli.sh windows

PLATFORM="${1:-linux}"

case "$PLATFORM" in
    linux|darwin)
        GOOS="$PLATFORM"
        ;;
    windows)
        GOOS="windows"
        ;;
    *)
        echo "Usage: $0 [linux|darwin|windows]"
        exit 1
        ;;
esac

echo "Target platform: $GOOS"

# stop running daemon first (only for native builds)
if [ "$GOOS" = "$(go env GOOS)" ]; then
    ./claw-cli stop 2>/dev/null || true
fi

# enable local xterm-go replace for build
sed -i 's|^// replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|' go.mod

# restore on exit
trap "sed -i 's|^replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|// replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|' go.mod" EXIT

echo "Building claw-cli for $GOOS..."
if [ "$GOOS" = "windows" ]; then
    GOOS=$GOOS go build -tags cli -o claw-cli.exe ./cmd/claw
    echo "Done: $(pwd)/claw-cli.exe"
else
    GOOS=$GOOS go build -tags cli -o claw-cli ./cmd/claw
    echo "Done: $(pwd)/claw-cli"
fi
