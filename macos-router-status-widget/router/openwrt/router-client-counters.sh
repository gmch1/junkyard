#!/bin/sh

client_counters_json() {
	command -v nlbw >/dev/null 2>&1 || {
		printf '[]'
		return 0
	}

	{
		awk '{
			name = ($4 == "*" ? "" : $4)
			printf "LEASE\t%s\t%s\t%s\n", tolower($2), $3, name
		}' /tmp/dhcp.leases 2>/dev/null

		ip -4 neigh show dev br-lan 2>/dev/null | awk '{
			for (i = 1; i <= NF; i++)
				if ($i == "lladdr")
					printf "NEIGH\t%s\t%s\n", tolower($(i + 1)), $1
		}'

		cat /tmp/hosts/dhcp.* 2>/dev/null | awk 'NF >= 2 {
			printf "HOST\t%s\t%s\n", $1, $2
		}'

		nlbw -c csv -g mac 2>/dev/null | awk -F '\t' 'NR > 1 {
			gsub(/"/, "", $1)
			printf "COUNT\t%s\t%s\t%s\n", tolower($1), $3, $5
		}'
	} | awk -F '\t' '
		function escape_json(value) {
			gsub(/\\/, "\\\\", value)
			gsub(/"/, "\\\"", value)
			gsub(/\r/, "", value)
			gsub(/\n/, "", value)
			return value
		}

		$1 == "LEASE" {
			known[$2] = 1
			address[$2] = $3
			if ($4 != "") name[$2] = $4
			next
		}
		$1 == "NEIGH" {
			known[$2] = 1
			if (address[$2] == "") address[$2] = $3
			next
		}
		$1 == "HOST" {
			hostname[$2] = $3
			next
		}
		$1 == "COUNT" {
			count++
			mac[count] = $2
			rx[count] = $3
			tx[count] = $4
			next
		}

		END {
			printf "["
			first = 1
			for (i = 1; i <= count; i++) {
				key = mac[i]
				if (!known[key] || key == "00:00:00:00:00:00") continue
				display = name[key]
				if (display == "") display = hostname[address[key]]
				if (display == "") display = address[key]
				if (display == "") display = key
				if (!first) printf ","
				printf "{\"mac\":\"%s\",\"name\":\"%s\",\"ip\":\"%s\",\"rx_bytes\":%s,\"tx_bytes\":%s}", \
					escape_json(key), escape_json(display), escape_json(address[key]), rx[i], tx[i]
				first = 0
			}
			printf "]"
		}
	'
}
