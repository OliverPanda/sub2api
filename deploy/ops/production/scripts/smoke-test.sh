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

payment_domain=$(env_get PAYMENT_DOMAIN || true)
payment_domain=${payment_domain:-pay.xddapi.org.uk}
[[ "$payment_domain" =~ ^[A-Za-z0-9.-]+$ ]] || die "invalid payment domain: ${payment_domain}"
payment_url="https://${payment_domain}"

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

log INFO "Checking Bridge health: ${public_url}/shop/healthz"
bridge_health=$(curl "${curl_common[@]}" "${public_url}/shop/healthz")
printf '%s' "$bridge_health" | jq -e '.status == "ok"' >/dev/null || {
  die "unexpected /shop/healthz response: ${bridge_health}"
}

log INFO "Checking Dujiao payment backend: ${payment_url}/health"
payment_code=$(curl --silent --show-error \
  --connect-timeout 10 --max-time 25 \
  --retry "$retries" --retry-delay 5 --retry-all-errors \
  -o /dev/null -w '%{http_code}' "${payment_url}/health")
[[ "$payment_code" =~ ^2[0-9][0-9]$ ]] || die "unexpected payment backend /health status: ${payment_code}"

log INFO "Checking /shop canonical redirect"
redirect_headers=$(curl --silent --show-error \
  --connect-timeout 10 --max-time 25 \
  --retry "$retries" --retry-delay 5 --retry-all-errors \
  -D - -o /dev/null "${public_url}/shop")
redirect_status=$(printf '%s\n' "$redirect_headers" | awk 'toupper($1) ~ /^HTTP\// {code=$2} END {print code}')
redirect_location=$(printf '%s\n' "$redirect_headers" | awk 'BEGIN{IGNORECASE=1} /^location:/ {sub(/\r$/, ""); print $2}' | tail -n 1)
[[ "$redirect_status" == "308" ]] || die "/shop must return 308, got ${redirect_status}"
[[ "$redirect_location" == "/shop/" ]] || die "/shop Location must be /shop/, got ${redirect_location}"

log INFO "Checking unsigned internal callback cannot reach Sub2API fallback"
callback_file=$(mktemp "${TMPDIR:-/tmp}/xdd-bridge-callback-smoke.XXXXXX")
callback_code=$(curl --silent --show-error \
  --connect-timeout 10 --max-time 25 \
  --retry "$retries" --retry-delay 5 --retry-all-errors \
  -o "$callback_file" -w '%{http_code}' \
  -H 'Content-Type: application/json' -X POST \
  --data '{"order_id":0,"status":"paid"}' \
  "${public_url}/internal/order-paid")
rm -f "$callback_file"
case "$callback_code" in
  401|503) ;;
  *) die "unsigned /internal/order-paid must return 401 or 503, got ${callback_code}" ;;
esac

log INFO "Public smoke test passed for ${public_url} and ${payment_url}"
