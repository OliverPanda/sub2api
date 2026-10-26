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

log INFO "Checking legacy /shop redirect to native payment"
redirect_headers=$(curl --silent --show-error \
  --connect-timeout 10 --max-time 25 \
  --retry "$retries" --retry-delay 5 --retry-all-errors \
  -D - -o /dev/null "${public_url}/shop")
redirect_status=$(printf '%s\n' "$redirect_headers" | awk 'toupper($1) ~ /^HTTP\// {code=$2} END {print code}')
redirect_location=$(printf '%s\n' "$redirect_headers" | awk 'BEGIN{IGNORECASE=1} /^location:/ {sub(/\r$/, ""); print $2}' | tail -n 1)
[[ "$redirect_status" == "308" ]] || die "/shop must return 308, got ${redirect_status}"
[[ "$redirect_location" == "/payment" ]] || die "/shop Location must be /payment, got ${redirect_location}"

log INFO "Checking retired internal callbacks are blocked"
callback_file=$(mktemp "${TMPDIR:-/tmp}/xdd-internal-callback-smoke.XXXXXX")
callback_code=$(curl --silent --show-error \
  --connect-timeout 10 --max-time 25 \
  --retry "$retries" --retry-delay 5 --retry-all-errors \
  -o "$callback_file" -w '%{http_code}' \
  -H 'Content-Type: application/json' -X POST \
  --data '{"order_id":0,"status":"paid"}' \
  "${public_url}/internal/order-paid")
rm -f "$callback_file"
[[ "$callback_code" == "404" ]] || die "retired /internal/order-paid must return 404, got ${callback_code}"

log INFO "Public smoke test passed for ${public_url}"
