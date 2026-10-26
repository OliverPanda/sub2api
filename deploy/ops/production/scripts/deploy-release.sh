#!/usr/bin/env bash
set -Eeuo pipefail

SUB2API_ROOT=${SUB2API_ROOT:-/opt/sub2api}
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh disable=SC1091
source "${SCRIPT_DIR}/lib.sh"

umask 077
mkdir -p "$LOG_DIR" "${SUB2API_ROOT}/state" "${SUB2API_ROOT}/releases" "${SUB2API_ROOT}/backups"
LOG_FILE="${LOG_FILE:-${LOG_DIR}/deploy.log}"
exec > >(tee -a "$LOG_FILE") 2>&1

MODE="deploy"
VERSION=""
SUB2API_DIGEST=""
TARGET_SUB2API_IMAGE=""
EXPECTED_REVISION=""
PREVIOUS_SUB2API_IMAGE=""
PREVIOUS_VERSION=""
PREVIOUS_SUB2API_DIGEST=""
PREVIOUS_REVISION=""
PREVIOUS_RELEASE_FILE=""
BACKUP_FILE=""
ROLLBACK_NEEDED=false

# OpenSSH invokes a forced command without appending the client command as
# arguments. SSH_ORIGINAL_COMMAND is the only accepted remote surface.
# Current protocol:
#   deploy v1.2.3 sha256:<sub2api> <40-character git revision>
# Temporary migration compatibility:
#   deploy v1.2.3 sha256:<sub2api> sha256:<ignored-bridge> <git-revision>
if [[ $# -eq 0 && -n "${SSH_ORIGINAL_COMMAND:-}" ]]; then
  read -r requested_mode requested_version requested_sub2api_digest \
    requested_third requested_fourth extra <<< "${SSH_ORIGINAL_COMMAND}" || true
  [[ -z "${extra:-}" ]] || die "too many arguments in forced command"
  if [[ "$requested_mode" == "deploy" ]]; then
    if [[ -n "${requested_fourth:-}" ]]; then
      # The third value is the retired Bridge digest. It is accepted only so
      # an in-flight GitHub workflow can finish during the migration.
      set -- "$requested_version" "$requested_sub2api_digest" "$requested_fourth"
    else
      set -- "$requested_version" "$requested_sub2api_digest" "${requested_third:-}"
    fi
  elif [[ "$requested_mode" == "rollback" ]]; then
    [[ -z "${requested_version:-}${requested_sub2api_digest:-}${requested_third:-}${requested_fourth:-}" ]] \
      || die "rollback does not accept arguments"
    set -- rollback
  else
    die "only deploy and rollback are permitted"
  fi
fi

if [[ ${1:-} == "deploy" ]]; then
  shift
fi

if [[ ${1:-} == "rollback" ]]; then
  MODE="rollback"
  shift
  [[ $# -eq 0 ]] || die "rollback does not accept arguments"
elif [[ $# -eq 3 ]]; then
  VERSION=$1
  SUB2API_DIGEST=$2
  EXPECTED_REVISION=$3
elif [[ $# -eq 4 ]]; then
  # Compatibility with the old four-value deploy protocol. The Bridge digest
  # has no runtime meaning now and is intentionally ignored.
  VERSION=$1
  SUB2API_DIGEST=$2
  EXPECTED_REVISION=$4
  log WARN "Ignoring retired Bridge digest argument"
else
  die "usage: deploy-release.sh deploy vMAJOR.MINOR.PATCH <sub2api-digest> <git-revision> | deploy-release.sh rollback"
fi

lock_timeout=$(env_get DEPLOY_LOCK_TIMEOUT_SECONDS || true)
lock_timeout=${lock_timeout:-300}
[[ "$lock_timeout" =~ ^[0-9]+$ ]] || die "DEPLOY_LOCK_TIMEOUT_SECONDS must be an integer"

exec 9>"${SUB2API_ROOT}/state/deploy.lock"
if ! flock -w "$lock_timeout" 9; then
  die "another deployment is still running"
fi

validate_digest() {
  [[ "$1" =~ ^sha256:[0-9a-f]{64}$ ]]
}

validate_revision() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]]
}

wait_service_healthy() {
  local service=$1 timeout=${2:-180} started=$SECONDS id status
  while (( SECONDS - started < timeout )); do
    id=$(compose ps -q "$service" 2>/dev/null || true)
    if [[ -n "$id" ]]; then
      status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id" 2>/dev/null || echo missing)
      case "$status" in
        healthy) return 0 ;;
        exited|dead|unhealthy)
          log ERROR "${service} entered state ${status}"
          compose logs --no-color --tail=120 "$service" || true
          return 1
          ;;
      esac
    fi
    sleep 3
  done
  log ERROR "${service} did not become healthy within ${timeout}s"
  compose logs --no-color --tail=120 "$service" || true
  return 1
}

start_if_missing() {
  local service=$1
  if [[ -z "$(compose ps -q "$service" 2>/dev/null || true)" ]]; then
    log INFO "Starting dependency ${service}"
    compose up -d --no-deps "$service"
  fi
}

ensure_dependencies() {
  local service
  for service in postgres redis; do
    start_if_missing "$service"
  done
  wait_service_healthy postgres 180
  wait_service_healthy redis 120
}

running_release_value() {
  local service=$1 format=$2 container_id
  container_id=$(compose ps -q "$service" 2>/dev/null || true)
  [[ -n "$container_id" ]] || return 0
  docker inspect --format "$format" "$container_id" 2>/dev/null || true
}

digest_from_image() {
  sed -n 's/.*@\(sha256:[0-9a-f]\{64\}\)$/\1/p'
}

write_release_env_file() {
  local file=$1 sub2api_image=$2 version=$3 sub2api_digest=$4 revision=$5
  local tmp="${file}.tmp"
  {
    printf 'SUB2API_IMAGE=%s\n' "$sub2api_image"
    printf 'RELEASE_VERSION=%s\n' "$version"
    printf 'RELEASE_SUB2API_DIGEST=%s\n' "$sub2api_digest"
    # Compatibility alias for older deployment tooling.
    printf 'RELEASE_DIGEST=%s\n' "$sub2api_digest"
    printf 'RELEASE_REVISION=%s\n' "$revision"
  } > "$tmp"
  chmod 0600 "$tmp"
  mv "$tmp" "$file"
}

capture_previous_release() {
  rm -f "${SUB2API_ROOT}/state/release.env.previous"

  if [[ -s "${SUB2API_ROOT}/state/current-sub2api-image" ]]; then
    PREVIOUS_SUB2API_IMAGE=$(<"${SUB2API_ROOT}/state/current-sub2api-image")
    PREVIOUS_VERSION=$(cat "${SUB2API_ROOT}/state/current-version" 2>/dev/null || true)
    PREVIOUS_SUB2API_DIGEST=$(cat "${SUB2API_ROOT}/state/current-sub2api-digest" 2>/dev/null || true)
    PREVIOUS_REVISION=$(cat "${SUB2API_ROOT}/state/current-revision" 2>/dev/null || true)
  elif [[ -s "${SUB2API_ROOT}/state/current-image" ]]; then
    PREVIOUS_SUB2API_IMAGE=$(<"${SUB2API_ROOT}/state/current-image")
    PREVIOUS_VERSION=$(cat "${SUB2API_ROOT}/state/current-version" 2>/dev/null || true)
    PREVIOUS_SUB2API_DIGEST=$(cat "${SUB2API_ROOT}/state/current-digest" 2>/dev/null || true)
    PREVIOUS_REVISION=$(cat "${SUB2API_ROOT}/state/current-revision" 2>/dev/null || true)
  elif [[ -f "$RELEASE_ENV" ]]; then
    PREVIOUS_SUB2API_IMAGE=$(env_get SUB2API_IMAGE "$RELEASE_ENV" || true)
    PREVIOUS_VERSION=$(env_get RELEASE_VERSION "$RELEASE_ENV" || true)
    PREVIOUS_SUB2API_DIGEST=$(env_get RELEASE_SUB2API_DIGEST "$RELEASE_ENV" || true)
    if [[ -z "$PREVIOUS_SUB2API_DIGEST" ]]; then
      PREVIOUS_SUB2API_DIGEST=$(env_get RELEASE_DIGEST "$RELEASE_ENV" || true)
    fi
    PREVIOUS_REVISION=$(env_get RELEASE_REVISION "$RELEASE_ENV" || true)
  fi

  # Recover from the live Sub2API container if an older release left only
  # partial metadata. Bridge metadata is deliberately ignored.
  if [[ -z "$PREVIOUS_SUB2API_IMAGE" ]]; then
    PREVIOUS_SUB2API_IMAGE=$(running_release_value sub2api '{{.Config.Image}}')
  fi
  if [[ -n "$PREVIOUS_SUB2API_IMAGE" ]]; then
    if [[ -z "$PREVIOUS_VERSION" ]]; then
      PREVIOUS_VERSION=$(running_release_value sub2api '{{index .Config.Labels "org.opencontainers.image.version"}}')
      if [[ -n "$PREVIOUS_VERSION" && "$PREVIOUS_VERSION" != v* ]]; then
        PREVIOUS_VERSION="v${PREVIOUS_VERSION}"
      fi
    fi
    if [[ -z "$PREVIOUS_SUB2API_DIGEST" ]]; then
      PREVIOUS_SUB2API_DIGEST=$(printf '%s\n' "$PREVIOUS_SUB2API_IMAGE" | digest_from_image)
    fi
    if [[ -z "$PREVIOUS_REVISION" ]]; then
      PREVIOUS_REVISION=$(running_release_value sub2api '{{index .Config.Labels "org.opencontainers.image.revision"}}')
    fi
  fi

  if [[ -n "$PREVIOUS_SUB2API_IMAGE" ]]; then
    if [[ "$PREVIOUS_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
      && validate_digest "$PREVIOUS_SUB2API_DIGEST" \
      && validate_revision "$PREVIOUS_REVISION"; then
      PREVIOUS_RELEASE_FILE="${SUB2API_ROOT}/state/release.env.previous"
      write_release_env_file "$PREVIOUS_RELEASE_FILE" "$PREVIOUS_SUB2API_IMAGE" \
        "$PREVIOUS_VERSION" "$PREVIOUS_SUB2API_DIGEST" "$PREVIOUS_REVISION"
    else
      log WARN "Previous release metadata is incomplete; automatic rollback is unavailable for this deployment"
      PREVIOUS_RELEASE_FILE=""
    fi
  fi
  log INFO "Previous release: ${PREVIOUS_VERSION:-none} sub2api=${PREVIOUS_SUB2API_DIGEST:-no-digest}"
}

write_release_env() {
  write_release_env_file "$RELEASE_ENV" "$TARGET_SUB2API_IMAGE" \
    "$VERSION" "$SUB2API_DIGEST" "$EXPECTED_REVISION"
}

record_current_release() {
  local release_file=${1:-$RELEASE_ENV}
  local sub2api_image version sub2api_digest revision
  sub2api_image=$(env_get SUB2API_IMAGE "$release_file" || true)
  version=$(env_get RELEASE_VERSION "$release_file" || true)
  sub2api_digest=$(env_get RELEASE_SUB2API_DIGEST "$release_file" || true)
  if [[ -z "$sub2api_digest" ]]; then
    sub2api_digest=$(env_get RELEASE_DIGEST "$release_file" || true)
  fi
  revision=$(env_get RELEASE_REVISION "$release_file" || true)
  printf '%s\n' "$sub2api_image" > "${SUB2API_ROOT}/state/current-sub2api-image"
  printf '%s\n' "$version" > "${SUB2API_ROOT}/state/current-version"
  printf '%s\n' "$sub2api_digest" > "${SUB2API_ROOT}/state/current-sub2api-digest"
  printf '%s\n' "$revision" > "${SUB2API_ROOT}/state/current-revision"
  # Legacy aliases remain readable by older read-only tooling.
  printf '%s\n' "$sub2api_image" > "${SUB2API_ROOT}/state/current-image"
  printf '%s\n' "$sub2api_digest" > "${SUB2API_ROOT}/state/current-digest"
  cp "$release_file" "${SUB2API_ROOT}/state/release.env.current"
  chmod 0600 \
    "${SUB2API_ROOT}/state/current-sub2api-image" \
    "${SUB2API_ROOT}/state/current-version" \
    "${SUB2API_ROOT}/state/current-sub2api-digest" \
    "${SUB2API_ROOT}/state/current-revision" \
    "${SUB2API_ROOT}/state/current-image" \
    "${SUB2API_ROOT}/state/current-digest" \
    "${SUB2API_ROOT}/state/release.env.current"
}

verify_image() {
  local image=$1 digest=$2 version=$3 revision=$4
  local label_version label_revision

  log INFO "Pulling Sub2API image ${image}"
  docker pull "$image"
  label_version=$(docker image inspect "$image" --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' 2>/dev/null || true)
  label_revision=$(docker image inspect "$image" --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' 2>/dev/null || true)
  [[ "$label_version" == "${version#v}" ]] \
    || die "Sub2API image version label '${label_version}' does not match ${version}"
  [[ "$label_revision" =~ ^[0-9a-f]{40}$ ]] \
    || die "Sub2API image revision label is missing or invalid"
  [[ "$label_revision" == "$revision" ]] \
    || die "Sub2API image revision label '${label_revision}' does not match expected ${revision}"

  case "$image" in
    *"@${digest}") ;;
    *) die "Sub2API image is not digest-pinned" ;;
  esac
}

rollback_to_previous() {
  local sub2api_image version sub2api_digest revision
  if [[ -z "$PREVIOUS_RELEASE_FILE" || ! -f "$PREVIOUS_RELEASE_FILE" ]]; then
    log ERROR "No previous release is available; automatic application rollback is impossible"
    return 1
  fi
  sub2api_image=$(env_get SUB2API_IMAGE "$PREVIOUS_RELEASE_FILE" || true)
  version=$(env_get RELEASE_VERSION "$PREVIOUS_RELEASE_FILE" || true)
  sub2api_digest=$(env_get RELEASE_SUB2API_DIGEST "$PREVIOUS_RELEASE_FILE" || true)
  if [[ -z "$sub2api_digest" ]]; then
    sub2api_digest=$(env_get RELEASE_DIGEST "$PREVIOUS_RELEASE_FILE" || true)
  fi
  revision=$(env_get RELEASE_REVISION "$PREVIOUS_RELEASE_FILE" || true)
  if [[ -z "$sub2api_image" ]] \
    || ! validate_digest "$sub2api_digest" \
    || ! validate_revision "$revision" \
    || [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    log ERROR "Previous release metadata is incomplete; manual intervention required"
    return 1
  fi

  log WARN "Rolling Sub2API back to ${version} (${sub2api_digest})"
  verify_image "$sub2api_image" "$sub2api_digest" "$version" "$revision" || return 1
  cp "$PREVIOUS_RELEASE_FILE" "$RELEASE_ENV"
  chmod 0600 "$RELEASE_ENV"
  if ! compose up -d --no-deps --force-recreate sub2api; then
    log ERROR "Could not recreate the previous Sub2API container"
    return 1
  fi
  if ! "$SCRIPT_DIR/wait-health.sh"; then
    log ERROR "Previous Sub2API image did not become healthy"
    return 1
  fi
  if ! compose up -d --no-deps --force-recreate caddy; then
    log ERROR "Could not restart Caddy after application rollback"
    return 1
  fi
  if ! wait_service_healthy caddy 120; then
    log ERROR "Caddy did not become healthy after application rollback"
    return 1
  fi
  if ! "$SCRIPT_DIR/smoke-test.sh"; then
    log ERROR "Previous Sub2API image failed the public smoke test"
    return 1
  fi

  record_current_release "$PREVIOUS_RELEASE_FILE"
  {
    printf '%s\trollback\t%s\t%s\tbackup=%s\n' \
      "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$version" "$sub2api_digest" "${BACKUP_FILE:-none}"
  } >> "${SUB2API_ROOT}/releases.log"
  log WARN "Application rollback to ${version} completed; databases were not restored"
  return 0
}

on_error() {
  local rc=$? line=$1 command=$2
  trap - ERR
  log ERROR "Deployment failed at line ${line}: ${command}"
  if [[ "$MODE" == "deploy" && "$ROLLBACK_NEEDED" == true ]]; then
    if [[ -z "$PREVIOUS_SUB2API_IMAGE" ]]; then
      log ERROR "First production deployment failed; no previous application image exists to roll back to"
      rm -f "$RELEASE_ENV"
    elif rollback_to_previous; then
      log ERROR "Automatic application rollback succeeded; database migrations were not reverted"
    else
      log ERROR "Automatic rollback failed; manual intervention is required"
    fi
  fi
  exit "$rc"
}
trap 'on_error $? $LINENO "$BASH_COMMAND"' ERR

if [[ "$MODE" == "rollback" ]]; then
  [[ -f "${SUB2API_ROOT}/state/release.env.previous" ]] || die "no previous release metadata found"
  PREVIOUS_RELEASE_FILE="${SUB2API_ROOT}/state/release.env.previous"
  PREVIOUS_SUB2API_IMAGE=$(env_get SUB2API_IMAGE "$PREVIOUS_RELEASE_FILE" || true)
  PREVIOUS_VERSION=$(env_get RELEASE_VERSION "$PREVIOUS_RELEASE_FILE" || true)
  PREVIOUS_SUB2API_DIGEST=$(env_get RELEASE_SUB2API_DIGEST "$PREVIOUS_RELEASE_FILE" || true)
  if [[ -z "$PREVIOUS_SUB2API_DIGEST" ]]; then
    PREVIOUS_SUB2API_DIGEST=$(env_get RELEASE_DIGEST "$PREVIOUS_RELEASE_FILE" || true)
  fi
  PREVIOUS_REVISION=$(env_get RELEASE_REVISION "$PREVIOUS_RELEASE_FILE" || true)
  validate_digest "$PREVIOUS_SUB2API_DIGEST" || die "previous Sub2API release digest is invalid"
  validate_revision "$PREVIOUS_REVISION" || die "previous release revision is invalid"
  ensure_dependencies
  BACKUP_FILE=$("$SCRIPT_DIR/backup-postgres.sh" "pre-rollback-${PREVIOUS_VERSION}")
  if [[ -f "$RELEASE_ENV" ]]; then
    cp -p "$RELEASE_ENV" "${SUB2API_ROOT}/state/release.env.failed.$(date -u +'%Y%m%dT%H%M%SZ')"
  fi
  rollback_to_previous || die "manual application rollback failed"
  printf 'ROLLBACK_OK version=%s sub2api=%s backup=%s\n' \
    "$PREVIOUS_VERSION" "$PREVIOUS_SUB2API_DIGEST" "$BACKUP_FILE"
  exit 0
fi

[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "production version must be vMAJOR.MINOR.PATCH"
validate_digest "$SUB2API_DIGEST" || die "sub2api digest must be sha256 followed by 64 lowercase hex characters"
validate_revision "$EXPECTED_REVISION" || die "git revision must be 40 lowercase hex characters"
[[ -r "$ENV_FILE" ]] || die "missing ${ENV_FILE}; run bootstrap-server.sh and configure secrets first"

GHCR_SUB2API_IMAGE_BASE=${GHCR_SUB2API_IMAGE_BASE:-${GHCR_IMAGE_BASE:-ghcr.io/oliverpanda/sub2api}}
TARGET_SUB2API_IMAGE="${GHCR_SUB2API_IMAGE_BASE}@${SUB2API_DIGEST}"

log INFO "Deploying ${VERSION}: sub2api=${SUB2API_DIGEST}"
capture_previous_release
if [[ -n "$PREVIOUS_VERSION" ]]; then
  [[ "$PREVIOUS_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "current release version is invalid: ${PREVIOUS_VERSION}"
  highest_version=$(printf '%s\n%s\n' "${PREVIOUS_VERSION#v}" "${VERSION#v}" | sort -V | tail -n 1)
  if [[ "$highest_version" != "${VERSION#v}" ]]; then
    die "refusing to downgrade production from ${PREVIOUS_VERSION} to ${VERSION}; use the rollback command for a deliberate application rollback"
  fi
fi
ensure_dependencies

# Always create a verified dump after dependencies are healthy. On the first
# release this captures the initialized database; later releases capture the
# real pre-deployment state. Restores remain a deliberate manual operation.
BACKUP_FILE=$("$SCRIPT_DIR/backup-postgres.sh" "pre-${VERSION}")

verify_image "$TARGET_SUB2API_IMAGE" "$SUB2API_DIGEST" "$VERSION" "$EXPECTED_REVISION"

write_release_env
ROLLBACK_NEEDED=true

log INFO "Recreating the Sub2API application container"
compose up -d --no-deps --force-recreate sub2api
"$SCRIPT_DIR/wait-health.sh"

log INFO "Applying the current Caddy configuration"
compose up -d --no-deps --force-recreate caddy
wait_service_healthy caddy 120

"$SCRIPT_DIR/smoke-test.sh"
record_current_release
ROLLBACK_NEEDED=false

{
  printf '%s\tdeploy\t%s\t%s\trevision=%s\tbackup=%s\n' \
    "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$VERSION" "$SUB2API_DIGEST" \
    "$EXPECTED_REVISION" "$BACKUP_FILE"
} >> "${SUB2API_ROOT}/releases.log"

printf 'DEPLOY_OK version=%s sub2api=%s revision=%s backup=%s\n' \
  "$VERSION" "$SUB2API_DIGEST" "$EXPECTED_REVISION" "$BACKUP_FILE"
