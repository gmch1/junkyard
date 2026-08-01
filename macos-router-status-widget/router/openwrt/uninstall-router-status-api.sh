#!/bin/sh
set -eu

uci -q delete uhttpd.router_status || true
uci commit uhttpd
rm -f /etc/router-status-api.token /etc/router-status-api.interface
rm -rf /www-router-status
/etc/init.d/uhttpd restart

echo "Router status API removed"
