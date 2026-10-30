#!/usr/bin/env bash
set -Eeuo pipefail

SUB2API_ROOT=${SUB2API_ROOT:-/opt/sub2api}
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh disable=SC1091
source "${SCRIPT_DIR}/lib.sh"

command -v curl >/dev/null || die "curl is required"
command -v jq >/dev/null || die "jq is required"

public_url=${1:-}
if [[ -z "$public_url" ]]; then
  domain=$(env_get PUBLIC_DOMAIN || true)
  domain=${domain:-api.xddapi.org.uk}
  public_url="https://${domain}"
fi
public_url=${public_url%/}
[[ "$public_url" =~ ^https://[A-Za-z0-9._:-]+$ ]] || die "invalid public URL: ${public_url}"

retries=$(env_get SMOKE_TEST_RETRIES || true)
retries=${retries:-6}
[[ "$retries" =~ ^[0-9]+$ ]] || die "SMOKE_TEST_RETRIES must be an integer"

curl_common=(
  --silent --show-error --fail
  --connect-timeout 10 --max-time 25
  --retry "$retries" --retry-delay 5 --retry-all-errors
)

log INFO "Running public health check: ${public_url}/health"
health_json=$(curl "${curl_common[@]}" "${public_url}/health")
printf '%s' "$health_json" | jq -e '.status == "ok"' >/dev/null || {
  die "unexpected /health response: ${health_json}"
}

log INFO "Running setup-status check: ${public_url}/setup/status"
setup_json=$(curl "${curl_common[@]}" "${public_url}/setup/status")
printf '%s' "$setup_json" | jq -e '.code == 0 and .data.needs_setup == false' >/dev/null || {
  die "unexpected /setup/status response: ${setup_json}"
}

log INFO "Public smoke test passed for ${public_url}"
