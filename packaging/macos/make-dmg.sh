#!/bin/bash
# make-dmg.sh <app-bundle> <output.dmg>
# Build a stylized drag-to-Applications DMG using the packaged background image.
# The layout (icon positions, window size, background) is applied via Finder and
# persisted into the DMG's .DS_Store.
set -e

APP="$1"
OUT="$2"
VOLNAME="CLIAnywhere"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BG="$SCRIPT_DIR/dmg-background.png"
BG_2X="$SCRIPT_DIR/dmg-background@2x.png"

if [ -z "$APP" ] || [ -z "$OUT" ]; then
    echo "usage: $0 <app-bundle> <output.dmg>" >&2
    exit 1
fi

STAGE_ROOT="$(mktemp -d)"
STAGE="$STAGE_ROOT/dmg"
mkdir -p "$STAGE/.background"
# ditto preserves the notarization ticket (xattr/bundle files) inside the .app;
# a plain cp -R may drop extended attributes.
ditto "$APP" "$STAGE/CLIAnywhere.app"
ln -s /Applications "$STAGE/Applications"
# 1x + @2x background: Finder picks the right one per screen so the window
# always maps 1:1 to the design size (660x400) regardless of display scale.
cp "$BG" "$STAGE/.background/dmg-background.png"
cp "$BG_2X" "$STAGE/.background/dmg-background@2x.png"

TMP_DMG="$STAGE_ROOT/tmp.dmg"
MOUNT_POINT="/Volumes/$VOLNAME"

# Detach a stale mount from a previous failed run, if any.
hdiutil detach "$MOUNT_POINT" -quiet >/dev/null 2>&1 || true

# 1. Build a read/write image so Finder can persist the layout into .DS_Store.
hdiutil create -volname "$VOLNAME" -srcfolder "$STAGE" -ov -format UDRW "$TMP_DMG"

# 2. Mount, lay out the icons with Finder, then unmount.
hdiutil attach "$TMP_DMG" -nobrowse -mountpoint "$MOUNT_POINT"
sleep 2
osascript <<'EOF'
tell application "Finder"
    tell disk "CLIAnywhere"
        open
        delay 1
        set current view of container window to icon view
        set toolbar visible of container window to false
        set statusbar visible of container window to false
        set viewOptions to the icon view options of container window
        set arrangement of viewOptions to not arranged
        set icon size of viewOptions to 96
        set background picture of viewOptions to file ".background:dmg-background.png"
        set the bounds of container window to {80, 80, 740, 480}
        set position of item "CLIAnywhere.app" of container window to {120, 160}
        set position of item "Applications" of container window to {430, 160}
        delay 1
        close
    end tell
end tell
EOF
sleep 2
hdiutil detach "$MOUNT_POINT" -quiet

# 3. Convert to compressed UDZO and output.
hdiutil convert "$TMP_DMG" -format UDZO -o "$OUT"

rm -rf "$STAGE_ROOT"
