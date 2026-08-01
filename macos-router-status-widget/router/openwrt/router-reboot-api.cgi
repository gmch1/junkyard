#!/bin/sh

TOKEN_FILE="/etc/router-status-api.token"

respond_json() {
	status="$1"
	body="$2"
	printf 'Status: %s\r\n' "$status"
	printf 'Content-Type: application/json\r\n'
	printf 'Cache-Control: no-store\r\n'
	printf 'X-Content-Type-Options: nosniff\r\n'
	printf 'Connection: close\r\n'
	printf '\r\n'
	printf '%s\n' "$body"
}

if [ "$REQUEST_METHOD" != "POST" ]; then
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

case "${CONTENT_LENGTH:-}" in
	*[!0-9]*|'')
		respond_json "400 Bad Request" '{"error":"invalid_content_length"}'
		exit 0
		;;
esac

if [ "$CONTENT_LENGTH" -gt 64 ]; then
	respond_json "413 Content Too Large" '{"error":"request_too_large"}'
	exit 0
fi

body=$(dd bs=1 count="$CONTENT_LENGTH" 2>/dev/null)
if [ "$body" != '{"action":"reboot"}' ]; then
	respond_json "400 Bad Request" '{"error":"invalid_action"}'
	exit 0
fi

if [ ! -x /sbin/reboot ]; then
	respond_json "503 Service Unavailable" '{"error":"reboot_unavailable"}'
	exit 0
fi

respond_json "202 Accepted" '{"accepted":true,"action":"reboot"}'
(sleep 1; /sbin/reboot) </dev/null >/dev/null 2>&1 &
exit 0
