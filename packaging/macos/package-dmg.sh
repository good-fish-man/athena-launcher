#!/usr/bin/env bash
set -euo pipefail

BINARY=${BINARY:?BINARY is required}
VERSION=${VERSION:?VERSION is required}
ARCH=${ARCH:?ARCH is required}
OUTPUT_DIR=${OUTPUT_DIR:-dist/installers}
HDIUTIL=${HDIUTIL:-hdiutil}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
app="$work/Athena.app"
contents="$app/Contents"
mkdir -p "$contents/MacOS" "$contents/Resources/licenses" "$OUTPUT_DIR"
cp "$BINARY" "$contents/MacOS/athena-launcher"
cp LICENSE NOTICE THIRD_PARTY_NOTICES.md "$contents/Resources/licenses/"
chmod 755 "$contents/MacOS/athena-launcher"

cat > "$contents/MacOS/Athena" <<'EOF'
#!/bin/sh
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
error_log=$(mktemp -t athena-launcher)
if "$base/athena-launcher" launch 2>"$error_log"; then
  rm -f "$error_log"
  exit 0
fi
message=$(cat "$error_log")
rm -f "$error_log"
/usr/bin/osascript -e 'on run argv' -e 'display alert "Athena could not start" message (item 1 of argv) as critical' -e 'end run' "$message\n\nLogs: ~/.athena/logs/launcher.log"
exit 1
EOF
chmod 755 "$contents/MacOS/Athena"

cat > "$contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDisplayName</key><string>Athena</string>
  <key>CFBundleExecutable</key><string>Athena</string>
  <key>CFBundleIconFile</key><string>athena</string>
  <key>CFBundleIdentifier</key><string>ai.athena.launcher</string>
  <key>CFBundleName</key><string>Athena</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
</dict>
</plist>
EOF

icon_png="$work/athena.svg.png"
if qlmanage -t -s 1024 -o "$work" packaging/athena.svg >/dev/null 2>&1 && [ -f "$icon_png" ]; then
  iconset="$work/athena.iconset"
  mkdir -p "$iconset"
  for size in 16 32 128 256 512; do
    sips -z "$size" "$size" "$icon_png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
    retina=$((size * 2))
    sips -z "$retina" "$retina" "$icon_png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$iconset" -o "$contents/Resources/athena.icns"
fi

codesign --force --deep --sign - "$app"
dmg_root="$work/dmg"
mkdir -p "$dmg_root"
cp -R "$app" "$dmg_root/Athena.app"
ln -s /Applications "$dmg_root/Applications"
output="$OUTPUT_DIR/Athena_${VERSION}_macOS_${ARCH}.dmg"
"$HDIUTIL" create -volname "Athena" -srcfolder "$dmg_root" -ov -format UDZO "$output" >/dev/null
[ -s "$output" ] || { echo "DMG was not created: $output" >&2; exit 1; }
echo "created $output"
