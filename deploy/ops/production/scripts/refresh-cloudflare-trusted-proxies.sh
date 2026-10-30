#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OUTPUT=${1:-"${SCRIPT_DIR}/../caddy/cloudflare-trusted-proxies.caddy"}
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

curl -fsSL --connect-timeout 10 --max-time 30 https://www.cloudflare.com/ips-v4 > "$TMP_DIR/v4"
curl -fsSL --connect-timeout 10 --max-time 30 https://www.cloudflare.com/ips-v6 > "$TMP_DIR/v6"

ranges=()
while IFS= read -r line; do
  line=${line//$'\r'/}
  [[ -z "$line" ]] && continue
  [[ "$line" =~ ^[0-9A-Fa-f:.]+/[0-9]{1,3}$ ]] || {
    printf 'Invalid Cloudflare CIDR: %s\n' "$line" >&2
    exit 1
  }
  ranges+=("$line")
done < <(cat "$TMP_DIR/v4" "$TMP_DIR/v6")

[[ ${#ranges[@]} -ge 2 ]] || {
  printf 'Cloudflare range download returned too few entries\n' >&2
  exit 1
}

mkdir -p "$(dirname "$OUTPUT")"
tmp_output="${OUTPUT}.tmp"
{
  printf '# Generated from https://www.cloudflare.com/ips-v4 and /ips-v6\n'
  printf '# at %s. Re-run this script when Cloudflare changes its ranges.\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  printf 'trusted_proxies static %s\n' "${ranges[*]}"
  printf 'trusted_proxies_strict\n'
  printf 'client_ip_headers CF-Connecting-IP X-Forwarded-For\n'
} > "$tmp_output"
chmod 0644 "$tmp_output"
mv "$tmp_output" "$OUTPUT"
printf 'Wrote %s Cloudflare CIDRs to %s\n' "${#ranges[@]}" "$OUTPUT"
