#!/bin/zsh
# 构建 KbKeepAlive.app（无需 Xcode 工程，swiftc 直接编译）
set -euo pipefail
cd "$(dirname "$0")"

APP_NAME=KbKeepAlive
OUT="build/$APP_NAME.app"

rm -rf build
mkdir -p "$OUT/Contents/MacOS" "$OUT/Contents/Resources"

swiftc -O -whole-module-optimization \
  -target arm64-apple-macos13.0 \
  -o "$OUT/Contents/MacOS/$APP_NAME" \
  Sources/*.swift

cp Info.plist "$OUT/Contents/Info.plist"
plutil -lint "$OUT/Contents/Info.plist" >/dev/null
if [[ -f AppIcon.icns ]]; then
  cp AppIcon.icns "$OUT/Contents/Resources/"
fi

# 签名身份：TCC 授权（蓝牙/辅助功能）绑定证书 CN，换证书授权就丢。
# 默认固定用下面这张；可用环境变量 IDENTITY 覆盖。固定证书不可用时退回
# "第一张 Apple Development"并明确警告（授权可能需重新授予），都没有才临时签名。
# 注意 grep 无匹配在 pipefail 下会中止脚本，必须 || true 兜底
DEFAULT_IDENTITY='Apple Development: 8613572157391 (U385KDAVH7)'
IDENTITY="${IDENTITY:-$DEFAULT_IDENTITY}"
if ! security find-identity -v -p codesigning 2>/dev/null | grep -qF "\"$IDENTITY\""; then
  IDENTITY=$(security find-identity -v -p codesigning 2>/dev/null \
    | grep -m1 '"Apple Development' | sed -E 's/.*"(.*)"$/\1/' || true)
  if [[ -n "${IDENTITY:-}" ]]; then
    echo "警告: 固定证书不可用，本次用 \"$IDENTITY\" 签名（TCC 授权可能需要重新授予）"
  fi
fi
if [[ -n "${IDENTITY:-}" ]]; then
  codesign --force --sign "$IDENTITY" "$OUT"
  echo "签名身份: $IDENTITY"
else
  codesign --force --sign - "$OUT"
  echo "警告: 未找到 Apple Development 证书，退回临时签名（系统授权会在下次构建失效）"
fi

codesign --verify --strict "$OUT"
echo "Designated requirement: $(codesign -d -r- "$OUT" 2>&1 | grep -i 'designated' | head -1 || true)"

echo "构建完成: $(pwd)/$OUT"
