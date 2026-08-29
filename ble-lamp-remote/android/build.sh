#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
sdk_root="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"

if [[ -z "$sdk_root" ]]; then
  echo "Set ANDROID_SDK_ROOT or ANDROID_HOME." >&2
  exit 1
fi

if [[ -n "${ANDROID_BUILD_TOOLS_VERSION:-}" ]]; then
  build_tools_dir="$sdk_root/build-tools/$ANDROID_BUILD_TOOLS_VERSION"
else
  build_tools_dir="$(find "$sdk_root/build-tools" -mindepth 1 -maxdepth 1 -type d -printf '%p\n' | sort -V | tail -n 1)"
fi
if [[ -n "${ANDROID_PLATFORM_VERSION:-}" ]]; then
  platform_dir="$sdk_root/platforms/android-$ANDROID_PLATFORM_VERSION"
else
  platform_dir="$(find "$sdk_root/platforms" -mindepth 1 -maxdepth 1 -type d -name 'android-*' -printf '%p\n' | sort -V | tail -n 1)"
fi
android_jar="$platform_dir/android.jar"

for tool in aapt2 d8 zipalign apksigner; do
  if [[ ! -x "$build_tools_dir/$tool" ]]; then
    echo "Missing Android build tool: $build_tools_dir/$tool" >&2
    exit 1
  fi
done

if [[ ! -f "$android_jar" ]]; then
  echo "Missing Android platform jar: $android_jar" >&2
  exit 1
fi

mkdir -p "$project_dir/build" "$project_dir/.debug"
tmp_dir="$(mktemp -d "$project_dir/build/tmp.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

unsigned_apk="$tmp_dir/ble-lamp-remote-unsigned.apk"
aligned_apk="$tmp_dir/ble-lamp-remote-aligned.apk"
classes_dir="$tmp_dir/classes"
dex_dir="$tmp_dir/dex"
keystore="${ANDROID_KEYSTORE_PATH:-$project_dir/.debug/ble-probe-debug.keystore}"
output_apk="${ANDROID_APK_OUTPUT:-$project_dir/build/ble-lamp-remote-debug.apk}"
key_alias="${ANDROID_KEY_ALIAS:-androiddebugkey}"
store_password="${ANDROID_KEYSTORE_PASSWORD:-android}"
key_password="${ANDROID_KEY_PASSWORD:-android}"

mkdir -p "$classes_dir" "$dex_dir" "$(dirname "$output_apk")"

"$build_tools_dir/aapt2" link \
  -I "$android_jar" \
  --manifest "$project_dir/AndroidManifest.xml" \
  -o "$unsigned_apk"

mapfile -t java_sources < <(find "$project_dir/src" -type f -name '*.java' -print)
javac -encoding UTF-8 -source 8 -target 8 -Xlint:-options \
  -classpath "$android_jar" \
  -d "$classes_dir" \
  "${java_sources[@]}"

mapfile -t class_files < <(find "$classes_dir" -type f -name '*.class' -print)
"$build_tools_dir/d8" \
  --min-api 23 \
  --lib "$android_jar" \
  --output "$dex_dir" \
  "${class_files[@]}"

zip -q -j "$unsigned_apk" "$dex_dir/classes.dex"
"$build_tools_dir/zipalign" -f 4 "$unsigned_apk" "$aligned_apk"

if [[ ! -f "$keystore" && -n "${ANDROID_KEYSTORE_PATH:-}" ]]; then
  echo "Configured Android keystore does not exist: $keystore" >&2
  exit 1
fi

if [[ ! -f "$keystore" ]]; then
  keytool -genkeypair \
    -keystore "$keystore" \
    -storepass "$store_password" \
    -alias "$key_alias" \
    -keypass "$key_password" \
    -dname "CN=BLE Lamp Remote Debug,O=Local Development,C=CN" \
    -keyalg RSA \
    -keysize 2048 \
    -validity 10000 \
    >/dev/null 2>&1
fi

export BLE_LAMP_BUILD_STORE_PASSWORD="$store_password"
export BLE_LAMP_BUILD_KEY_PASSWORD="$key_password"
"$build_tools_dir/apksigner" sign \
  --ks "$keystore" \
  --ks-key-alias "$key_alias" \
  --ks-pass env:BLE_LAMP_BUILD_STORE_PASSWORD \
  --key-pass env:BLE_LAMP_BUILD_KEY_PASSWORD \
  --out "$output_apk" \
  "$aligned_apk"

"$build_tools_dir/apksigner" verify "$output_apk"
echo "$output_apk"
