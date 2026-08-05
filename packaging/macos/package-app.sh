#!/usr/bin/env bash
set -euo pipefail

BINARY=${BINARY:?BINARY is required}
VERSION=${VERSION:?VERSION is required}
APP_DIR=${APP_DIR:?APP_DIR is required}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
app="$work/Athena.app"
contents="$app/Contents"
mkdir -p "$contents/MacOS" "$contents/Resources/licenses"
cp "$BINARY" "$contents/MacOS/athena-launcher"
cp LICENSE NOTICE THIRD_PARTY_NOTICES.md "$contents/Resources/licenses/"
chmod 755 "$contents/MacOS/athena-launcher"

cat > "$contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDisplayName</key><string>Athena</string>
  <key>CFBundleExecutable</key><string>athena-launcher</string>
  <key>CFBundleIdentifier</key><string>ai.athena.launcher</string>
  <key>CFBundleName</key><string>Athena</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSLocationWhenInUseUsageDescription</key><string>Athena uses your location only when you ask location-dependent questions such as local weather.</string>
  <key>NSAppleEventsUsageDescription</key><string>Athena controls an application only after you ask the Agent to do so and approve the requested desktop action.</string>
  <key>NSMicrophoneUsageDescription</key><string>Athena uses the microphone only when you start voice input or a voice conversation.</string>
  <key>NSSpeechRecognitionUsageDescription</key><string>Athena converts your speech to text only when you start voice input or a voice conversation.</string>
</dict>
</plist>
EOF

codesign --force --deep --sign - --entitlements packaging/macos/entitlements.plist "$app"
mkdir -p "$(dirname "$APP_DIR")"
rm -rf "$APP_DIR"
mv "$app" "$APP_DIR"
echo "created $APP_DIR"
