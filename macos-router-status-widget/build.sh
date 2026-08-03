#!/bin/zsh
set -euo pipefail

project_dir="${0:A:h}"
app_dir="$project_dir/build/RouterStatusWidget.app"
module_cache="$project_dir/.build/module-cache"

if [[ -d /Applications/Xcode.app/Contents/Developer ]]; then
  export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
fi

mkdir -p "$app_dir/Contents/MacOS" "$app_dir/Contents/Resources" "$module_cache"
cp "$project_dir/Info.plist" "$app_dir/Contents/Info.plist"

xcrun swiftc \
  -O \
  -parse-as-library \
  -module-cache-path "$module_cache" \
  -framework AppKit \
  -framework Foundation \
  "$project_dir/Sources/RouterStatusWidget.swift" \
  -o "$app_dir/Contents/MacOS/RouterStatusWidget"

codesign --force --deep --sign - "$app_dir"
echo "$app_dir"
