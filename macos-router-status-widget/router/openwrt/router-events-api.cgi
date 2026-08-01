#!/bin/sh

TOKEN_FILE="/etc/router-status-api.token"
WAN_INTERFACE="eth1"

if [ -r /etc/router-status-api.interface ]; then
	IFS= read -r WAN_INTERFACE < /etc/router-status-api.interface
fi

respond_json() {
	status="$1"
	body="$2"
	printf 'Status: %s\r\n' "$status"
	printf 'Content-Type: application/json\r\n'
	printf 'Cache-Control: no-store\r\n'
	printf 'X-Content-Type-Options: nosniff\r\n'
	printf '\r\n'
	printf '%s\n' "$body"
}

if [ "$REQUEST_METHOD" != "GET" ]; then
	respond_json "405 Method Not Allowed" '{"error":"method_not_allowed"}'
	exit 0
fi

if [ ! -r "$TOKEN_FILE" ]; then
	respond_json "503 Service Unavailable" '{"error":"token_unavailable"}'
	exit 0
fi

IFS= read -r expected_token < "$TOKEN_FILE"
case "${HTTP_AUTHORIZATION:-}" in
	"Bearer "*) provided_token="${HTTP_AUTHORIZATION#Bearer }" ;;
	*) provided_token="" ;;
esac

if [ -z "$expected_token" ] || [ "$provided_token" != "$expected_token" ]; then
	respond_json "401 Unauthorized" '{"error":"unauthorized"}'
	exit 0
fi

case "$WAN_INTERFACE" in
	*[!A-Za-z0-9_.:-]*|'')
		respond_json "500 Internal Server Error" '{"error":"invalid_interface"}'
		exit 0
		;;
esac

net_path="/sys/class/net/$WAN_INTERFACE"
if [ ! -r "$net_path/statistics/rx_bytes" ]; then
	respond_json "503 Service Unavailable" '{"error":"wan_interface_unavailable"}'
	exit 0
fi

snapshot() {
	set -- $(sed -n '1s/^cpu[[:space:]]*//p' /proc/stat)
	cpu_total=0
	for value in "$@"; do
		cpu_total=$((cpu_total + value))
	done
	cpu_idle=$(( ${4:-0} + ${5:-0} ))

	set -- $(awk '
		/MemTotal:/ { total=$2 }
		/MemAvailable:/ { available=$2 }
		END { print total, available }
	' /proc/meminfo)
	mem_total_kb="${1:-0}"
	mem_available_kb="${2:-0}"

	rx_bytes=$(tr -d '\n' < "$net_path/statistics/rx_bytes")
	tx_bytes=$(tr -d '\n' < "$net_path/statistics/tx_bytes")
	link_mbps=$(tr -d '\n' < "$net_path/speed" 2>/dev/null)
	uptime_seconds=$(awk '{ print int($1) }' /proc/uptime)
	temperature_c="-1"
	for thermal_zone in /sys/class/thermal/thermal_zone*; do
		[ -r "$thermal_zone/temp" ] || continue
		temperature_millidegrees=$(tr -d '\n' < "$thermal_zone/temp")
		temperature_c=$(awk -v value="$temperature_millidegrees" 'BEGIN { printf "%.1f", value / 1000 }')
		break
	done

	printf '{"version":1,"cpu_total":%s,"cpu_idle":%s,"mem_total_kb":%s,"mem_available_kb":%s,"rx_bytes":%s,"tx_bytes":%s,"link_mbps":%s,"uptime_seconds":%s,"temperature_c":%s}' \
		"$cpu_total" "$cpu_idle" "$mem_total_kb" "$mem_available_kb" \
		"$rx_bytes" "$tx_bytes" "${link_mbps:--1}" "$uptime_seconds" "$temperature_c"
}

trap 'exit 0' HUP INT PIPE TERM

printf 'Status: 200 OK\r\n'
printf 'Content-Type: text/event-stream\r\n'
printf 'Cache-Control: no-cache, no-store\r\n'
printf 'X-Accel-Buffering: no\r\n'
printf 'X-Content-Type-Options: nosniff\r\n'
printf 'Connection: keep-alive\r\n'
printf '\r\n'
printf 'retry: 2000\n\n'

while :; do
	printf 'event: snapshot\n'
	printf 'data: '
	snapshot
	printf '\n\n' || exit 0
	sleep 5
done
