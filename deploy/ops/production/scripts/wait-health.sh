#!/usr/bin/env bash
set -Eeuo pipefail

SUB2API_ROOT=${SUB2API_ROOT:-/opt/sub2api}
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh disable=SC1091
source "${SCRIPT_DIR}/lib.sh"

timeout_seconds=${1:-$(env_get DEPLOY_HEALTH_TIMEOUT_SECONDS || true)}
timeout_seconds=${timeout_seconds:-300}
[[ "$timeout_seconds" =~ ^[0-9]+$ ]] || die "health timeout must be an integer"

wait_one() {
  local service=$1 started=$SECONDS container_id last_status="unknown"
  while (( SECONDS - started < timeout_seconds )); do
    container_id=$(compose ps -q "$service" 2>/dev/null || true)
    if [[ -z "$container_id" ]]; then
      last_status="missing"
    else
      last_status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container_id" 2>/dev/null || echo missing)
      case "$last_status" in
        healthy)
          log INFO "${service} is healthy after $((SECONDS - started))s"
          return 0
          ;;
        exited|dead|unhealthy)
          log ERROR "${service} entered state: ${last_status}"
          compose logs --no-color --tail=160 "$service" || true
          return 1
          ;;
      esac
    fi
    sleep 3
  done

  log ERROR "${service} did not become healthy within ${timeout_seconds}s (last state: ${last_status})"
  compose logs --no-color --tail=160 "$service" || true
  return 1
}

wait_one sub2api
