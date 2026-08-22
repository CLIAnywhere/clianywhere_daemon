#!/bin/bash
# Build claw (web) for macOS — output: ./CLIAnywhere.app in script dir.
# (bundles claw binary + Info.plist + AppIcon.icns)
set -e
cd "$(dirname "$0")"

# Pause before exit when run by double-click (Terminal.app closes window on exit).
pause_if_terminal() {
    if [ -t 0 ]; then
        echo ""
        echo "Press Enter to close..."
        read -r _
    fi
}
trap pause_if_terminal EXIT

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

# --- Package .app bundle ----------------------------------------------------
# Assemble the CLIAnywhere.app bundle, output to the current directory
APP="CLIAnywhere.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

cp claw "$APP/Contents/MacOS/claw"
cp packaging/macos/Info.plist "$APP/Contents/Info.plist"

# Generate AppIcon.icns (if iconutil is available)
ICONSET=/tmp/AppIcon.iconset
rm -rf "$ICONSET"
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
    sips -z $s $s packaging/macos/app_icon_1024.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
    d=$((s*2))
    sips -z $d $d packaging/macos/app_icon_1024.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
sips -z 1024 1024 packaging/macos/app_icon_1024.png --out "$ICONSET/icon_512x512@2x.png" >/dev/null
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
rm -rf "$ICONSET"

# Fill in the version number (placeholder -> date version)
VER="$(date +%Y%m%d)"
sed -i '' "s/VERSION_PLACEHOLDER/${VER}/g" "$APP/Contents/Info.plist"

echo "[done]  $(pwd)/$APP"
