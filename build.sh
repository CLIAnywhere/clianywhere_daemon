#!/bin/bash
set -e
cd "$(dirname "$0")"

# stop running daemon first
if [ -f claw ]; then
    ./claw stop 2>/dev/null || true
fi

# enable local xterm-go replace for build
sed -i 's|^// replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|' go.mod

# restore on exit
trap "sed -i 's|^replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|// replace github.com/CLIAnywhere/xterm-go => ../xterm-go-latest|' go.mod" EXIT

echo "Building claw..."
go build -o claw ./cmd/claw
echo "Done: $(pwd)/claw"
