#!/usr/bin/env bash
set -euo pipefail

BINARY=${BINARY:?BINARY is required}
VERSION=${VERSION:?VERSION is required}
ARCH=${ARCH:?ARCH is required}
APPIMAGETOOL=${APPIMAGETOOL:?APPIMAGETOOL is required}
OUTPUT_DIR=${OUTPUT_DIR:-dist/installers}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
appdir="$work/Athena.AppDir"
mkdir -p "$appdir/usr/bin" "$appdir/usr/share/icons/hicolor/scalable/apps" "$OUTPUT_DIR"
cp "$BINARY" "$appdir/usr/bin/athena-launcher"
chmod 755 "$appdir/usr/bin/athena-launcher"
cp packaging/athena.svg "$appdir/athena.svg"
cp packaging/athena.svg "$appdir/usr/share/icons/hicolor/scalable/apps/athena.svg"

cat > "$appdir/AppRun" <<'EOF'
#!/bin/sh
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$base/usr/bin/athena-launcher" launch
EOF
chmod 755 "$appdir/AppRun"

cat > "$appdir/athena.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Athena
Comment=Local AI agent workspace
Exec=athena-launcher launch
Icon=athena
Categories=Development;Utility;
Terminal=false
X-AppImage-Version=$VERSION
EOF

output="$OUTPUT_DIR/Athena_${VERSION}_linux_${ARCH}.AppImage"
ARCH="$ARCH" APPIMAGE_EXTRACT_AND_RUN=1 "$APPIMAGETOOL" "$appdir" "$output"
chmod 755 "$output"
echo "created $output"
