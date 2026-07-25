#!/bin/bash
# Build claw (web) for macOS — output: ./claw in script dir.
set -e
cd "$(dirname "$0")"

REQUIRED_MAJOR=1
REQUIRED_MINOR=25

# --- Go toolchain check -----------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
    echo "Error: Go is not installed or not in PATH."
    echo "       Please install Go ${REQUIRED_MAJOR}.${REQUIRED_MINOR} or later:"
    echo "         https://go.dev/dl/"
    echo "       or use Homebrew:  brew install go"
    exit 1
fi

GO_VERSION="$(go env GOVERSION)"
GO_VERSION="${GO_VERSION#go}"
GO_MAJOR="${GO_VERSION%%.*}"
REST="${GO_VERSION#*.}"
GO_MINOR="${REST%%.*}"

GO_OK=0
if [ "$GO_MAJOR" -gt "$REQUIRED_MAJOR" ]; then GO_OK=1; fi
if [ "$GO_MAJOR" -eq "$REQUIRED_MAJOR" ] && [ "$GO_MINOR" -ge "$REQUIRED_MINOR" ]; then GO_OK=1; fi

if [ "$GO_OK" -ne 1 ]; then
    echo "Error: Go ${GO_VERSION} is too old (need ${REQUIRED_MAJOR}.${REQUIRED_MINOR}+)."
    echo "       Please update Go from https://go.dev/dl/"
    exit 1
fi
echo "[go] ${GO_VERSION} detected, OK."

# --- Build ------------------------------------------------------------------
echo "[build] claw (darwin/$(go env GOARCH), web)"
GOOS=darwin GOARCH="$(go env GOARCH)" go build -tags web -o claw ./cmd/claw
echo "[done]  $(pwd)/claw"
