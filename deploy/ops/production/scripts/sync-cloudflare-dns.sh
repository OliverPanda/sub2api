#!/usr/bin/env bash
set -Eeuo pipefail

zone_id=${CLOUDFLARE_ZONE_ID:-}
api_token=${CLOUDFLARE_API_TOKEN:-}
record_name=${CLOUDFLARE_DNS_RECORD_NAME:-api.xddapi.org.uk}
ipv4=${ORIGIN_IPV4:-149.104.31.126}
ipv6=${ORIGIN_IPV6:-2001:df1:7880:2::3a7}
required=${CLOUDFLARE_DNS_REQUIRED:-false}
configure_zone=${CLOUDFLARE_CONFIGURE_ZONE:-false}

if [[ -z "$zone_id" || -z "$api_token" ]]; then
  if [[ "$required" == "true" ]]; then
    printf 'Cloudflare credentials are required but CLOUDFLARE_ZONE_ID or CLOUDFLARE_API_TOKEN is missing.\n' >&2
    exit 1
  fi
  printf 'Cloudflare credentials are not configured; skipping DNS sync.\n' >&2
  exit 0
fi

command -v curl >/dev/null || { printf 'curl is required\n' >&2; exit 1; }
command -v jq >/dev/null || { printf 'jq is required\n' >&2; exit 1; }
[[ "$zone_id" =~ ^[0-9a-fA-F]{32}$ ]] || { printf 'invalid Cloudflare zone ID\n' >&2; exit 1; }
[[ "$record_name" =~ ^[A-Za-z0-9.-]+$ ]] || { printf 'invalid DNS record name\n' >&2; exit 1; }
[[ "$ipv4" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || { printf 'invalid IPv4 address\n' >&2; exit 1; }
[[ "$ipv6" =~ ^[0-9A-Fa-f:.]+$ ]] || { printf 'invalid IPv6 address\n' >&2; exit 1; }

api_base="https://api.cloudflare.com/client/v4"
auth_header="Authorization: Bearer ${api_token}"
common_curl=(
  --silent --show-error --fail-with-body
  --connect-timeout 10 --max-time 30
  --retry 4 --retry-delay 2 --retry-all-errors
)

cloudflare_json() {
  jq -e '.success == true' >/dev/null
}

set_zone_setting() {
  local setting=$1 value=$2 result
  if ! result=$(curl "${common_curl[@]}" \
    -X PATCH -H "$auth_header" -H 'Content-Type: application/json' \
    --data "$(jq -n --arg value "$value" '{value: $value}')" \
    "${api_base}/zones/${zone_id}/settings/${setting}"); then
    printf 'Cloudflare setting %s update failed: %s\n' "$setting" "$result" >&2
    exit 1
  fi
  if ! cloudflare_json <<<"$result"; then
    printf 'Cloudflare setting %s update returned an error: %s\n' "$setting" "$result" >&2
    exit 1
  fi
  printf 'Ensured Cloudflare zone setting %s=%s\n' "$setting" "$value" >&2
}

upsert() {
  local type=$1 content=$2 query result record_id payload ids=() extra_id
  if ! query=$(curl "${common_curl[@]}" \
    -H "$auth_header" \
    -G --data-urlencode "type=${type}" --data-urlencode "name=${record_name}" \
    "${api_base}/zones/${zone_id}/dns_records"); then
    printf 'Cloudflare DNS query failed for %s: %s\n' "$type" "$query" >&2
    exit 1
  fi
  if ! cloudflare_json <<<"$query"; then
    printf 'Cloudflare DNS query returned an error for %s: %s\n' "$type" "$query" >&2
    exit 1
  fi

  mapfile -t ids < <(jq -r '.result[].id' <<<"$query")
  record_id=${ids[0]:-}
  payload=$(jq -n \
    --arg type "$type" \
    --arg name "$record_name" \
    --arg content "$content" \
    '{type: $type, name: $name, content: $content, ttl: 1, proxied: true, comment: "Managed by Sub2API production deploy"}')

  if [[ -n "$record_id" ]]; then
    if ! result=$(curl "${common_curl[@]}" \
      -X PATCH -H "$auth_header" -H 'Content-Type: application/json' \
      --data "$payload" "${api_base}/zones/${zone_id}/dns_records/${record_id}"); then
      printf 'Cloudflare DNS update failed for %s: %s\n' "$type" "$result" >&2
      exit 1
    fi
    printf 'Updated proxied %s record %s -> %s\n' "$type" "$record_name" "$content" >&2
  else
    if ! result=$(curl "${common_curl[@]}" \
      -X POST -H "$auth_header" -H 'Content-Type: application/json' \
      --data "$payload" "${api_base}/zones/${zone_id}/dns_records"); then
      printf 'Cloudflare DNS create failed for %s: %s\n' "$type" "$result" >&2
      exit 1
    fi
    printf 'Created proxied %s record %s -> %s\n' "$type" "$record_name" "$content" >&2
  fi
  if ! cloudflare_json <<<"$result"; then
    printf 'Cloudflare DNS update returned an error for %s: %s\n' "$type" "$result" >&2
    exit 1
  fi

  # Keep exactly one managed record for each address family.
  for extra_id in "${ids[@]:1}"; do
    if ! result=$(curl "${common_curl[@]}" \
      -X DELETE -H "$auth_header" \
      "${api_base}/zones/${zone_id}/dns_records/${extra_id}"); then
      printf 'Cloudflare duplicate DNS delete failed for %s (%s): %s\n' "$type" "$extra_id" "$result" >&2
      exit 1
    fi
    cloudflare_json <<<"$result" || { printf 'Cloudflare duplicate DNS delete returned an error for %s\n' "$type" >&2; exit 1; }
    printf 'Deleted duplicate %s record %s\n' "$type" "$extra_id" >&2
  done
}

if [[ "$configure_zone" == "true" ]]; then
  set_zone_setting ssl strict
  set_zone_setting always_use_https on
fi

upsert A "$ipv4"
upsert AAAA "$ipv6"
