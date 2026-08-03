#!/bin/sh
set -eu

uci -q delete uhttpd.router_status || true
uci commit uhttpd
rm -f /etc/router-status-api.token /etc/router-status-api.interface
rm -rf /www-router-status
/etc/init.d/uhttpd restart

if [ -r /etc/router-status-api.nlbw-refresh ] && uci -q get nlbwmon.@nlbwmon[0] >/dev/null; then
	IFS= read -r previous_refresh < /etc/router-status-api.nlbw-refresh
	case "$previous_refresh" in
		*[!0-9smhd]*|'') ;;
		*)
			uci set nlbwmon.@nlbwmon[0].refresh_interval="$previous_refresh"
			uci commit nlbwmon
			/etc/init.d/nlbwmon restart
			;;
	esac
fi
rm -f /etc/router-status-api.nlbw-refresh

echo "Router status API removed"
