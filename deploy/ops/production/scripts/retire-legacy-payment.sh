#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_NAME=${COMPOSE_PROJECT_NAME:-xdd-api}
legacy_services=(bridge bridge-postgres dujiao-next dujiao-postgres dujiao-redis)

log() { printf '[retire-legacy-payment] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

command -v docker >/dev/null || die "docker is required"

# This is intentionally conservative: stop and list only, never remove
# containers, volumes, bind mounts, images, or data.
for service in "${legacy_services[@]}"; do
  mapfile -t container_ids < <(
    docker ps -q \
      --filter "label=com.docker.compose.project=${PROJECT_NAME}" \
      --filter "label=com.docker.compose.service=${service}"
  )
  if [[ ${#container_ids[@]} -eq 0 ]]; then
    log "${service}: no running container"
    continue
  fi
  for container_id in "${container_ids[@]}"; do
    project_label=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$container_id" 2>/dev/null || true)
    service_label=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$container_id" 2>/dev/null || true)
    [[ "$project_label" == "$PROJECT_NAME" && "$service_label" == "$service" ]] \
      || die "refusing to stop ${container_id}: Compose labels do not match ${PROJECT_NAME}/${service}"
    log "Stopping ${PROJECT_NAME}/${service} container ${container_id}"
    docker stop "$container_id" >/dev/null
  done
done

log "Legacy payment containers stopped. Volumes and bind-mounted data were left untouched."
log "For a permanent rollback, preserve the stopped containers until the native payment flow has been accepted."
