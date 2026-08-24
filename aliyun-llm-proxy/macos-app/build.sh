#!/bin/zsh

set -euo pipefail

project_dir="${0:A:h}"
build_dir="$project_dir/build"
app_dir="$build_dir/AliyunLLMProxy.app"
module_cache="$build_dir/module-cache"
backend_dir="$project_dir/Backend"

command -v go >/dev/null || { echo "Go 1.22+ is required." >&2; exit 1; }
command -v xcrun >/dev/null || { echo "Xcode Command Line Tools are required." >&2; exit 1; }

if [[ -d /Applications/Xcode.app/Contents/Developer ]]; then
  export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
fi

rm -rf "$app_dir" "$module_cache"
mkdir -p "$app_dir/Contents/MacOS" "$app_dir/Contents/Resources" "$module_cache"
cp "$project_dir/Info.plist" "$app_dir/Contents/Info.plist"

for arch in amd64 arm64; do
  (
    cd "$backend_dir"
    CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build \
      -trimpath \
      -ldflags="-s -w" \
      -o "$build_dir/backend-$arch" \
      .
  )
done
lipo -create "$build_dir/backend-amd64" "$build_dir/backend-arm64" \
  -output "$app_dir/Contents/MacOS/AliyunLLMProxyBackend"
chmod 755 "$app_dir/Contents/MacOS/AliyunLLMProxyBackend"

for arch in x86_64 arm64; do
  xcrun swiftc \
    -O \
    -parse-as-library \
    -swift-version 5 \
    -target "$arch-apple-macos13.0" \
    -module-cache-path "$module_cache/$arch" \
    -framework AppKit \
    -framework Foundation \
    "$project_dir/Sources/AliyunLLMProxyApp.swift" \
    -o "$build_dir/AliyunLLMProxy-$arch"
done
lipo -create "$build_dir/AliyunLLMProxy-x86_64" "$build_dir/AliyunLLMProxy-arm64" \
  -output "$app_dir/Contents/MacOS/AliyunLLMProxy"

if [[ -f "$project_dir/Resources/AppIcon.png" ]]; then
  iconset="$build_dir/AppIcon.iconset"
  rm -rf "$iconset"
  mkdir -p "$iconset"
  for size in 16 32 128 256 512; do
    sips -z "$size" "$size" "$project_dir/Resources/AppIcon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
    double=$((size * 2))
    sips -z "$double" "$double" "$project_dir/Resources/AppIcon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$iconset" -o "$app_dir/Contents/Resources/AppIcon.icns"
fi

plutil -lint "$app_dir/Contents/Info.plist"
codesign --force --sign - "$app_dir/Contents/MacOS/AliyunLLMProxyBackend"
codesign --force --deep --sign - "$app_dir"
codesign --verify --deep --strict "$app_dir"

archive="$build_dir/AliyunLLMProxy-macOS-universal.zip"
ditto -c -k --sequesterRsrc --keepParent "$app_dir" "$archive"
echo "$archive"
