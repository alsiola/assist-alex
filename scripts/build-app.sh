#!/bin/sh
# Builds Assist.app from the Swift package and signs it ad hoc, so macOS
# gives it its own identity for notifications. Usage: build-app.sh [DEST]
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
dest=${1:-"$HOME/Applications"}
app="$root/build/Assist.app"

swift build -c release --package-path "$root/app"
bin=$(swift build -c release --package-path "$root/app" --show-bin-path)/Assist

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin" "$app/Contents/MacOS/Assist"
version=$(git -C "$root" describe --always --dirty 2>/dev/null || echo dev)
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>com.alsiola.assist</string>
  <key>CFBundleName</key><string>Assist</string>
  <key>CFBundleDisplayName</key><string>Assist</string>
  <key>CFBundleExecutable</key><string>Assist</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>0.1.0</string>
  <key>CFBundleVersion</key><string>$version</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <key>NSPrincipalClass</key><string>NSApplication</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST
codesign --force --sign - "$app" >/dev/null
mkdir -p "$dest"
rm -rf "$dest/Assist.app"
cp -R "$app" "$dest/Assist.app"
echo "installed $dest/Assist.app"
