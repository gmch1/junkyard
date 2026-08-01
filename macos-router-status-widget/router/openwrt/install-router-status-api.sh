#!/bin/sh
set -eu

API_SOURCE="${1:-/tmp/router-status-api.cgi}"
TOKEN_SOURCE="${2:-/tmp/router-status-api.token}"
REBOOT_SOURCE="${3:-/tmp/router-reboot-api.cgi}"
EVENTS_SOURCE="${4:-/tmp/router-events-api.cgi}"
LAN_IP="${ROUTER_STATUS_LAN_IP:-192.168.31.1}"
API_PORT="${ROUTER_STATUS_API_PORT:-8099}"
WAN_INTERFACE="${ROUTER_STATUS_WAN_INTERFACE:-eth1}"
DOCROOT="/www-router-status"

case "$LAN_IP" in
	*[!0-9.]*|'') echo "Invalid LAN IPv4 address" >&2; exit 1 ;;
esac
case "$API_PORT" in
	*[!0-9]*|'') echo "Invalid API port" >&2; exit 1 ;;
esac
case "$WAN_INTERFACE" in
	*[!A-Za-z0-9_.:-]*|'') echo "Invalid WAN interface" >&2; exit 1 ;;
esac

[ -s "$API_SOURCE" ] || { echo "Missing API script: $API_SOURCE" >&2; exit 1; }
[ -s "$TOKEN_SOURCE" ] || { echo "Missing token: $TOKEN_SOURCE" >&2; exit 1; }
[ -s "$REBOOT_SOURCE" ] || { echo "Missing reboot API script: $REBOOT_SOURCE" >&2; exit 1; }
[ -s "$EVENTS_SOURCE" ] || { echo "Missing events API script: $EVENTS_SOURCE" >&2; exit 1; }
[ -x /sbin/reboot ] || { echo "Missing executable: /sbin/reboot" >&2; exit 1; }

[ -e /etc/config/uhttpd.pre-router-status ] || \
	cp /etc/config/uhttpd /etc/config/uhttpd.pre-router-status

mkdir -p "$DOCROOT/cgi-bin"
cp "$API_SOURCE" "$DOCROOT/cgi-bin/status"
chmod 755 "$DOCROOT/cgi-bin/status"
cp "$REBOOT_SOURCE" "$DOCROOT/cgi-bin/reboot"
chmod 755 "$DOCROOT/cgi-bin/reboot"
cp "$EVENTS_SOURCE" "$DOCROOT/cgi-bin/events"
chmod 755 "$DOCROOT/cgi-bin/events"
cp "$TOKEN_SOURCE" /etc/router-status-api.token
chmod 600 /etc/router-status-api.token
printf '%s\n' "$WAN_INTERFACE" > /etc/router-status-api.interface
chmod 600 /etc/router-status-api.interface

uci -q delete uhttpd.router_status || true
uci set uhttpd.router_status=uhttpd
uci add_list uhttpd.router_status.listen_http="$LAN_IP:$API_PORT"
uci set uhttpd.router_status.home="$DOCROOT"
uci set uhttpd.router_status.cgi_prefix='/cgi-bin'
uci set uhttpd.router_status.no_dirlists='1'
uci set uhttpd.router_status.no_symlinks='1'
uci set uhttpd.router_status.max_requests='4'
uci set uhttpd.router_status.max_connections='8'
uci set uhttpd.router_status.script_timeout='3600'
uci set uhttpd.router_status.network_timeout='15'
uci set uhttpd.router_status.http_keepalive='10'
uci set uhttpd.router_status.tcp_keepalive='1'
uci commit uhttpd
/etc/init.d/uhttpd restart

echo "Router status API listening on http://$LAN_IP:$API_PORT/cgi-bin/status"
echo "Router events API listening on http://$LAN_IP:$API_PORT/cgi-bin/events"
echo "Router reboot API listening on http://$LAN_IP:$API_PORT/cgi-bin/reboot"
