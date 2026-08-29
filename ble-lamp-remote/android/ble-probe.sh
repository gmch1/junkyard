#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
package_name="com.ming.lightprobe"
activity_name="$package_name/.MainActivity"
adb_bin="${ADB_BIN:-adb}"

adb_args=()
if [[ -n "${BLE_PROBE_SERIAL:-}" ]]; then
  adb_args=(-s "$BLE_PROBE_SERIAL")
fi

adb_run() {
  "$adb_bin" "${adb_args[@]}" "$@"
}

usage() {
  echo "Usage: $0 install | counter 0-255 | rotate-api-token | start [min-rssi] | advertise SERVICE_DATA_HEX [duration-ms] | advertise-mfg MANUFACTURER_DATA_HEX [duration-ms] | advertise-dual MANUFACTURER_DATA_HEX SERVICE_DATA_HEX [duration-ms] | mark LABEL | logs [seconds] [output-file] | stop | status"
}

ensure_device() {
  local count
  count="$(adb_run devices | awk '$2 == "device" {count++} END {print count+0}')"
  if [[ "$count" -ne 1 && -z "${BLE_PROBE_SERIAL:-}" ]]; then
    echo "Expected exactly one ADB device; set BLE_PROBE_SERIAL when multiple devices are online." >&2
    adb_run devices -l >&2
    exit 1
  fi
  adb_run get-state >/dev/null
}

install_app() {
  ensure_device
  local apk api current_user remote_apk use_root
  apk="$($project_dir/build.sh)"
  use_root=false
  if ! adb_run install -r "$apk"; then
    if adb_run shell su -c id 2>/dev/null | grep -q 'uid=0'; then
      use_root=true
      remote_apk="/data/local/tmp/ble-lamp-remote-debug.apk"
      echo "Normal ADB install was blocked; retrying through the rooted package manager."
      adb_run push "$apk" "$remote_apk" >/dev/null
      adb_run shell su -c "pm install -r '$remote_apk'"
      adb_run shell su -c "rm -f '$remote_apk'"
    else
      echo "ADB install was blocked. Enable Install via USB on the phone and retry." >&2
      exit 1
    fi
  fi
  api="$(adb_run shell getprop ro.build.version.sdk | tr -d '\r')"
  current_user="$(adb_run shell am get-current-user | tr -d '\r')"
  if adb_run shell su -c id 2>/dev/null | grep -q 'uid=0'; then
    use_root=true
  fi

  if (( api >= 31 )); then
    grant_permission "$use_root" "$current_user" android.permission.BLUETOOTH_SCAN
    grant_permission "$use_root" "$current_user" android.permission.BLUETOOTH_CONNECT
    grant_permission "$use_root" "$current_user" android.permission.BLUETOOTH_ADVERTISE
  else
    grant_permission "$use_root" "$current_user" android.permission.ACCESS_FINE_LOCATION
  fi
  if (( api >= 33 )); then
    grant_permission "$use_root" "$current_user" android.permission.POST_NOTIFICATIONS
  fi
  adb_run shell am start -S -n "$activity_name"
  echo "Lamp remote app launched."
}

grant_permission() {
  local use_root="$1"
  local current_user="$2"
  local permission="$3"
  if [[ "$use_root" == true ]]; then
    adb_run shell su -c \
      "pm grant --user '$current_user' '$package_name' '$permission'"
  else
    adb_run shell pm grant --user "$current_user" "$package_name" "$permission"
  fi
}

start_scan() {
  ensure_device
  local min_rssi="${1:--75}"
  adb_run shell am start -S \
    -n "$activity_name" \
    --es command start \
    --ei min_rssi "$min_rssi"
  echo "BLE scan requested with minimum RSSI $min_rssi dBm."
}

write_marker() {
  ensure_device
  local label="${1:-}"
  if [[ -z "$label" ]]; then
    echo "Marker label is required." >&2
    exit 1
  fi
  adb_run shell am start \
    -n "$activity_name" \
    --es command mark \
    --es label "$label" \
    >/dev/null
  echo "Marker written: $label"
}

advertise_command() {
  ensure_device
  local service_data="${1:-}"
  local duration_ms="${2:-2000}"
  if [[ ! "$service_data" =~ ^[0-9A-Fa-f]{48}$ ]]; then
    echo "Service data must be exactly 24 bytes (48 hexadecimal characters)." >&2
    exit 1
  fi
  adb_run shell am start \
    -n "$activity_name" \
    --es command advertise \
    --es service_data "$service_data" \
    --ei duration_ms "$duration_ms" \
    >/dev/null
  echo "BLE command advertisement requested for $duration_ms ms."
}

advertise_manufacturer_command() {
  ensure_device
  local manufacturer_data="${1:-}"
  local duration_ms="${2:-2000}"
  if [[ ! "$manufacturer_data" =~ ^[0-9A-Fa-f]{54}$ ]]; then
    echo "Manufacturer data must be exactly 27 bytes (54 hexadecimal characters)." >&2
    exit 1
  fi
  adb_run shell am start \
    -n "$activity_name" \
    --es command advertise \
    --es manufacturer_data "$manufacturer_data" \
    --ei duration_ms "$duration_ms" \
    >/dev/null
  echo "BLE manufacturer advertisement requested for $duration_ms ms."
}

set_counter() {
  ensure_device
  local counter="${1:-}"
  if [[ ! "$counter" =~ ^([0-9]|[1-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])$ ]]; then
    echo "Counter must be an integer from 0 through 255." >&2
    exit 1
  fi
  adb_run shell am start \
    -n "$activity_name" \
    --es command set-counter \
    --ei counter "$counter" \
    >/dev/null
  echo "Phone transmission counter synchronized to $counter."
}

rotate_api_token() {
  ensure_device
  adb_run shell am start \
    -n "$activity_name" \
    --es command rotate-api-token \
    >/dev/null
  echo "LAN API token rotated; copy the new token from the phone app."
}

advertise_dual_command() {
  ensure_device
  local manufacturer_data="${1:-}"
  local service_data="${2:-}"
  local duration_ms="${3:-2000}"
  if [[ ! "$manufacturer_data" =~ ^[0-9A-Fa-f]{54}$ ]]; then
    echo "Manufacturer data must be exactly 27 bytes (54 hexadecimal characters)." >&2
    exit 1
  fi
  if [[ ! "$service_data" =~ ^[0-9A-Fa-f]{48}$ ]]; then
    echo "Service data must be exactly 24 bytes (48 hexadecimal characters)." >&2
    exit 1
  fi
  adb_run shell am start \
    -n "$activity_name" \
    --es command advertise \
    --es manufacturer_data "$manufacturer_data" \
    --es service_data "$service_data" \
    --ei duration_ms "$duration_ms" \
    >/dev/null
  echo "Dual BLE advertisement requested for $duration_ms ms."
}

show_logs() {
  ensure_device
  local seconds="${1:-0}"
  local output="${2:-}"
  mkdir -p "$project_dir/captures"
  if [[ -z "$output" ]]; then
    output="$project_dir/captures/ble-$(date '+%Y%m%d-%H%M%S').jsonl"
  fi
  echo "Writing BLE_PROBE JSON lines to $output"
  if [[ "$seconds" =~ ^[1-9][0-9]*$ ]]; then
    timeout --signal=INT "${seconds}s" \
      "$adb_bin" "${adb_args[@]}" logcat -T 1 -v raw -s BLE_PROBE:I '*:S' \
      | awk '/^\{/' \
      | tee "$output" || [[ "$?" -eq 124 || "$?" -eq 130 ]]
  else
    "$adb_bin" "${adb_args[@]}" logcat -T 1 -v raw -s BLE_PROBE:I '*:S' \
      | awk '/^\{/' \
      | tee "$output"
  fi
}

stop_scan() {
  ensure_device
  adb_run shell am start -n "$activity_name" --es command stop
  echo "BLE scan stopped."
}

show_status() {
  ensure_device
  local current_user
  current_user="$(adb_run shell am get-current-user | tr -d '\r')"
  if adb_run shell dumpsys activity services "$package_name/.BleScanService" \
      | grep -q "$package_name"; then
    echo "BLE lamp diagnostic scanner is running."
  else
    echo "BLE lamp diagnostic scanner is not running."
  fi
  adb_run shell dumpsys package "$package_name" | awk -v user="User $current_user:" '
    /versionName=/ {print}
    index($0, user) {in_user=1; next}
    in_user && /^    User [0-9]+:/ {exit}
    in_user && /BLUETOOTH_SCAN: granted=|BLUETOOTH_CONNECT: granted=|BLUETOOTH_ADVERTISE: granted=|POST_NOTIFICATIONS: granted=/ {print}
  '
}

case "${1:-}" in
  install)
    install_app
    ;;
  start)
    start_scan "${2:--75}"
    ;;
  advertise)
    advertise_command "${2:-}" "${3:-2000}"
    ;;
  advertise-mfg)
    advertise_manufacturer_command "${2:-}" "${3:-2000}"
    ;;
  advertise-dual)
    advertise_dual_command "${2:-}" "${3:-}" "${4:-2000}"
    ;;
  counter)
    set_counter "${2:-}"
    ;;
  rotate-api-token)
    rotate_api_token
    ;;
  mark)
    write_marker "${2:-}"
    ;;
  logs)
    show_logs "${2:-0}" "${3:-}"
    ;;
  stop)
    stop_scan
    ;;
  status)
    show_status
    ;;
  *)
    usage
    exit 1
    ;;
esac
