#!/usr/bin/env bash
# Shared helpers for the production deployment scripts.
# The caller must set SUB2API_ROOT before sourcing this file.

: "${SUB2API_ROOT:?SUB2API_ROOT must be set before sourcing lib.sh}"

COMPOSE_FILE="${COMPOSE_FILE:-${SUB2API_ROOT}/compose.yml}"
ENV_FILE="${ENV_FILE:-${SUB2API_ROOT}/.env}"
RELEASE_ENV="${RELEASE_ENV:-${SUB2API_ROOT}/state/release.env}"
LOG_DIR="${LOG_DIR:-${SUB2API_ROOT}/logs}"

log() {
  local level="${1:-INFO}"
  shift || true
  printf '%s [%s] %s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$level" "$*" >&2
}

die() {
  log ERROR "$*"
  exit 1
}

env_get() {
  local key="$1" file="${2:-$ENV_FILE}"
  [[ -r "$file" ]] || return 0
  awk -v key="$key" '
    index($0, key "=") == 1 {
      sub(/^[^=]*=/, "")
      sub(/\r$/, "")
      print
      exit
    }
  ' "$file"
}

compose() {
  local args=(
    --project-directory "$SUB2API_ROOT"
    --env-file "$ENV_FILE"
    -f "$COMPOSE_FILE"
  )
  local sub2api_image="${SUB2API_IMAGE:-}" bridge_image="${BRIDGE_IMAGE:-}"
  if [[ -f "$RELEASE_ENV" ]]; then
    args+=(--env-file "$RELEASE_ENV")
    sub2api_image=${sub2api_image:-$(env_get SUB2API_IMAGE "$RELEASE_ENV" || true)}
    bridge_image=${bridge_image:-$(env_get BRIDGE_IMAGE "$RELEASE_ENV" || true)}
  fi

  # The old single-image release metadata has no BRIDGE_IMAGE. Keep Compose
  # parseable during the one-time migration without starting either application
  # image from the placeholder. Command-local values do not leak into later
  # Compose calls.
  sub2api_image=${sub2api_image:-ghcr.io/oliverpanda/sub2api:not-deployed}
  bridge_image=${bridge_image:-ghcr.io/oliverpanda/sub2api-bridge:not-deployed}
  SUB2API_IMAGE="$sub2api_image" BRIDGE_IMAGE="$bridge_image" \
    docker compose "${args[@]}" "$@"
}

mkdir -p "$LOG_DIR"
