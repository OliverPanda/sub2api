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
DIGEST=""
TARGET_IMAGE=""
EXPECTED_REVISION=""
PREVIOUS_IMAGE=""
PREVIOUS_VERSION=""
PREVIOUS_DIGEST=""
PREVIOUS_RELEASE_FILE=""
BACKUP_FILE=""
ROLLBACK_NEEDED=false

# OpenSSH invokes a forced command without appending the client command as
# arguments. SSH_ORIGINAL_COMMAND is the only accepted remote surface:
#   deploy v1.2.3 sha256:<64 hex characters> <40-character git revision>
if [[ $# -eq 0 && -n "${SSH_ORIGINAL_COMMAND:-}" ]]; then
  read -r requested_mode requested_version requested_digest requested_revision extra <<< "${SSH_ORIGINAL_COMMAND}" || true
  [[ "$requested_mode" == "deploy" && -z "${extra:-}" ]] || die "only 'deploy <version> <digest> [git-revision]' is permitted"
  set -- "$requested_version" "$requested_digest"
  if [[ -n "${requested_revision:-}" ]]; then
    set -- "$requested_version" "$requested_digest" "$requested_revision"
  fi
fi

if [[ ${1:-} == "deploy" ]]; then
  shift
fi

if [[ ${1:-} == "rollback" ]]; then
  MODE="rollback"
  shift
  [[ $# -eq 0 ]] || die "rollback does not accept arguments"
elif [[ $# -eq 2 || $# -eq 3 ]]; then
  VERSION=$1
  DIGEST=$2
  EXPECTED_REVISION=${3:-}
else
  die "usage: deploy-release.sh deploy vMAJOR.MINOR.PATCH sha256:<digest> [git-revision] | deploy-release.sh rollback"
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
  [[ -z "$1" || "$1" =~ ^[0-9a-f]{40}$ ]]
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

ensure_dependencies() {
  local postgres_id redis_id
  postgres_id=$(compose ps -q postgres 2>/dev/null || true)
  redis_id=$(compose ps -q redis 2>/dev/null || true)
  if [[ -z "$postgres_id" || -z "$redis_id" ]]; then
    log INFO "Starting PostgreSQL and Redis"
    compose up -d --no-deps postgres redis
  fi
  wait_service_healthy postgres 180
  wait_service_healthy redis 120
}

capture_previous_release() {
  rm -f "${SUB2API_ROOT}/state/release.env.previous"

  # current-image/current-digest are written only after a successful health
  # check and smoke test, so they take precedence over release.env (which may
  # describe a failed attempt if a previous automatic rollback also failed).
  if [[ -s "${SUB2API_ROOT}/state/current-image" ]]; then
    PREVIOUS_IMAGE=$(<"${SUB2API_ROOT}/state/current-image")
    PREVIOUS_VERSION=$(cat "${SUB2API_ROOT}/state/current-version" 2>/dev/null || true)
    PREVIOUS_DIGEST=$(cat "${SUB2API_ROOT}/state/current-digest" 2>/dev/null || true)
  elif [[ -f "$RELEASE_ENV" ]]; then
    PREVIOUS_IMAGE=$(env_get SUB2API_IMAGE "$RELEASE_ENV" || true)
    PREVIOUS_VERSION=$(env_get RELEASE_VERSION "$RELEASE_ENV" || true)
    PREVIOUS_DIGEST=$(env_get RELEASE_DIGEST "$RELEASE_ENV" || true)
  fi

  if [[ -n "$PREVIOUS_IMAGE" ]]; then
    PREVIOUS_RELEASE_FILE="${SUB2API_ROOT}/state/release.env.previous"
    {
      printf 'SUB2API_IMAGE=%s\n' "$PREVIOUS_IMAGE"
      printf 'RELEASE_VERSION=%s\n' "$PREVIOUS_VERSION"
      printf 'RELEASE_DIGEST=%s\n' "$PREVIOUS_DIGEST"
      printf 'RELEASE_REVISION=%s\n' "$(if [[ -s "${SUB2API_ROOT}/state/current-revision" ]]; then cat "${SUB2API_ROOT}/state/current-revision"; fi)"
    } > "$PREVIOUS_RELEASE_FILE"
    chmod 0600 "$PREVIOUS_RELEASE_FILE"
  fi
  log INFO "Previous release: ${PREVIOUS_VERSION:-none} ${PREVIOUS_DIGEST:-no-digest}"
}

write_release_env() {
  local image=$1 version=$2 digest=$3 revision=$4 tmp="${RELEASE_ENV}.tmp"
  {
    printf 'SUB2API_IMAGE=%s\n' "$image"
    printf 'RELEASE_VERSION=%s\n' "$version"
    printf 'RELEASE_DIGEST=%s\n' "$digest"
    printf 'RELEASE_REVISION=%s\n' "$revision"
  } > "$tmp"
  chmod 0600 "$tmp"
  mv "$tmp" "$RELEASE_ENV"
}

record_current_release() {
  local release_file=${1:-$RELEASE_ENV}
  local image version digest revision
  image=$(env_get SUB2API_IMAGE "$release_file" || true)
  version=$(env_get RELEASE_VERSION "$release_file" || true)
  digest=$(env_get RELEASE_DIGEST "$release_file" || true)
  revision=$(env_get RELEASE_REVISION "$release_file" || true)
  printf '%s\n' "$image" > "${SUB2API_ROOT}/state/current-image"
  printf '%s\n' "$version" > "${SUB2API_ROOT}/state/current-version"
  printf '%s\n' "$digest" > "${SUB2API_ROOT}/state/current-digest"
  printf '%s\n' "$revision" > "${SUB2API_ROOT}/state/current-revision"
  cp "$release_file" "${SUB2API_ROOT}/state/release.env.current"
  chmod 0600 "${SUB2API_ROOT}"/state/current-* "${SUB2API_ROOT}/state/release.env.current"
}

rollback_to_previous() {
  local image version digest revision
  if [[ -z "$PREVIOUS_RELEASE_FILE" || ! -f "$PREVIOUS_RELEASE_FILE" ]]; then
    log ERROR "No previous release is available; automatic application rollback is impossible"
    return 1
  fi
  image=$(env_get SUB2API_IMAGE "$PREVIOUS_RELEASE_FILE" || true)
  version=$(env_get RELEASE_VERSION "$PREVIOUS_RELEASE_FILE" || true)
  digest=$(env_get RELEASE_DIGEST "$PREVIOUS_RELEASE_FILE" || true)
  revision=$(env_get RELEASE_REVISION "$PREVIOUS_RELEASE_FILE" || true)
  if [[ -z "$image" ]] || ! validate_digest "$digest"; then
    log ERROR "Previous release metadata is incomplete; manual intervention required"
    return 1
  fi

  log WARN "Rolling application back to ${version} (${digest})"
  if ! docker pull "$image"; then
    log ERROR "Could not pull previous image ${image}"
    return 1
  fi
  cp "$PREVIOUS_RELEASE_FILE" "$RELEASE_ENV"
  chmod 0600 "$RELEASE_ENV"
  if ! compose up -d --no-deps --force-recreate sub2api; then
    log ERROR "Could not recreate the previous application container"
    return 1
  fi
  if ! "$SCRIPT_DIR/wait-health.sh"; then
    log ERROR "Previous application image did not become healthy"
    return 1
  fi
  if ! compose up -d --no-deps caddy; then
    log ERROR "Could not restart Caddy after application rollback"
    return 1
  fi
  if ! wait_service_healthy caddy 120; then
    log ERROR "Caddy did not become healthy after application rollback"
    return 1
  fi
  if ! "$SCRIPT_DIR/smoke-test.sh"; then
    log ERROR "Previous application image failed the public smoke test"
    return 1
  fi

  record_current_release "$PREVIOUS_RELEASE_FILE"
  {
    printf '%s\trollback\t%s\t%s\tbackup=%s\n' \
      "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$version" "$digest" "${BACKUP_FILE:-none}"
  } >> "${SUB2API_ROOT}/releases.log"
  log WARN "Application rollback to ${version} completed; the database was not restored"
  return 0
}

on_error() {
  local rc=$? line=$1 command=$2
  trap - ERR
  log ERROR "Deployment failed at line ${line}: ${command}"
  if [[ "$MODE" == "deploy" && "$ROLLBACK_NEEDED" == true ]]; then
    if [[ -z "$PREVIOUS_IMAGE" ]]; then
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
  PREVIOUS_IMAGE=$(env_get SUB2API_IMAGE "$PREVIOUS_RELEASE_FILE" || true)
  PREVIOUS_VERSION=$(env_get RELEASE_VERSION "$PREVIOUS_RELEASE_FILE" || true)
  PREVIOUS_DIGEST=$(env_get RELEASE_DIGEST "$PREVIOUS_RELEASE_FILE" || true)
  validate_digest "$PREVIOUS_DIGEST" || die "previous release digest is invalid"
  ensure_dependencies
  BACKUP_FILE=$("$SCRIPT_DIR/backup-postgres.sh" "pre-rollback-${PREVIOUS_VERSION}")
  if [[ -f "$RELEASE_ENV" ]]; then
    cp -p "$RELEASE_ENV" "${SUB2API_ROOT}/state/release.env.failed.$(date -u +'%Y%m%dT%H%M%SZ')"
  fi
  rollback_to_previous || die "manual application rollback failed"
  printf 'ROLLBACK_OK version=%s digest=%s backup=%s\n' "$PREVIOUS_VERSION" "$PREVIOUS_DIGEST" "$BACKUP_FILE"
  exit 0
fi

[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "production version must be vMAJOR.MINOR.PATCH"
validate_digest "$DIGEST" || die "digest must be sha256 followed by 64 lowercase hex characters"
validate_revision "$EXPECTED_REVISION" || die "git revision must be 40 lowercase hex characters when provided"
[[ -r "$ENV_FILE" ]] || die "missing ${ENV_FILE}; run bootstrap-server.sh and configure secrets first"

GHCR_IMAGE_BASE=${GHCR_IMAGE_BASE:-ghcr.io/oliverpanda/sub2api}
TARGET_IMAGE="${GHCR_IMAGE_BASE}@${DIGEST}"

log INFO "Deploying Sub2API ${VERSION} with digest ${DIGEST}"
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
# release this captures the empty initialized database; on later releases it
# captures the real pre-deployment state. Never infer readiness from host-side
# PostgreSQL file ownership, which belongs to the container's postgres user.
BACKUP_FILE=$("$SCRIPT_DIR/backup-postgres.sh" "pre-${VERSION}")

log INFO "Pulling ${TARGET_IMAGE}"
docker pull "$TARGET_IMAGE"

label_version=$(docker image inspect "$TARGET_IMAGE" --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' 2>/dev/null || true)
label_revision=$(docker image inspect "$TARGET_IMAGE" --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' 2>/dev/null || true)
[[ "$label_version" == "${VERSION#v}" ]] || die "image version label '${label_version}' does not match ${VERSION}"
[[ "$label_revision" =~ ^[0-9a-f]{40}$ ]] || die "image revision label is missing or invalid"
if [[ -n "$EXPECTED_REVISION" && "$label_revision" != "$EXPECTED_REVISION" ]]; then
  die "image revision label '${label_revision}' does not match expected ${EXPECTED_REVISION}"
fi

write_release_env "$TARGET_IMAGE" "$VERSION" "$DIGEST" "$label_revision"
ROLLBACK_NEEDED=true

log INFO "Recreating the application container"
compose up -d --no-deps --force-recreate sub2api
"$SCRIPT_DIR/wait-health.sh"

log INFO "Ensuring Caddy is running"
compose up -d --no-deps caddy
wait_service_healthy caddy 120

"$SCRIPT_DIR/smoke-test.sh"
record_current_release
ROLLBACK_NEEDED=false

{
  printf '%s\tdeploy\t%s\t%s\trevision=%s\tbackup=%s\n' \
    "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$VERSION" "$DIGEST" "$label_revision" "$BACKUP_FILE"
} >> "${SUB2API_ROOT}/releases.log"

printf 'DEPLOY_OK version=%s digest=%s revision=%s backup=%s\n' \
  "$VERSION" "$DIGEST" "$label_revision" "$BACKUP_FILE"
