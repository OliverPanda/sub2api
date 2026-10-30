#!/usr/bin/env bash
set -Eeuo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  printf 'refresh-cloudflare-ranges.sh must run as root.\n' >&2
  exit 1
fi

SUB2API_ROOT=${SUB2API_ROOT:-/opt/sub2api}
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh disable=SC1091
source "${SCRIPT_DIR}/lib.sh"

ranges_file="${SUB2API_ROOT}/caddy/cloudflare-trusted-proxies.caddy"
mkdir -p "$(dirname "$ranges_file")"
old_checksum=$(sha256sum "$ranges_file" 2>/dev/null | awk '{print $1}' || true)

"${SCRIPT_DIR}/refresh-cloudflare-trusted-proxies.sh" "$ranges_file"
"${SCRIPT_DIR}/configure-firewall.sh"

new_checksum=$(sha256sum "$ranges_file" | awk '{print $1}')
if [[ "$old_checksum" != "$new_checksum" ]]; then
  caddy_id=$(compose ps -q caddy 2>/dev/null || true)
  if [[ -n "$caddy_id" ]]; then
    log INFO "Cloudflare ranges changed; restarting Caddy to load the new trusted-proxy list"
    compose restart caddy >/dev/null
  else
    log INFO "Cloudflare ranges changed; Caddy is not running yet"
  fi
else
  log INFO "Cloudflare ranges are unchanged"
fi
