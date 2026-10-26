#!/usr/bin/env bash
set -Eeuo pipefail

SUB2API_ROOT=${SUB2API_ROOT:-/opt/sub2api}
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh disable=SC1091
source "${SCRIPT_DIR}/lib.sh"

label=${1:-manual}
label=$(printf '%s' "$label" | tr -c 'A-Za-z0-9._-' '_' | cut -c1-80)
backup_dir="${SUB2API_ROOT}/backups"
retention_days=$(env_get BACKUP_RETENTION_DAYS || true)
retention_days=${retention_days:-14}
[[ "$retention_days" =~ ^[0-9]+$ ]] || die "BACKUP_RETENTION_DAYS must be an integer"

mkdir -p "$backup_dir"
chmod 0700 "$backup_dir"

stamp=$(date -u +'%Y%m%dT%H%M%SZ')
bundle="${backup_dir}/sub2api-${stamp}-${label}-$$"
mkdir -m 0700 "$bundle"

cleanup() {
  rm -rf "$bundle"
}
trap cleanup EXIT

backup_database() {
  local service=$1
  local name=$2
  local tmp="${bundle}/${name}.dump.tmp"
  local final="${bundle}/${name}.dump"

  if [[ -z "$(compose ps -q "$service" 2>/dev/null || true)" ]]; then
    die "${service} is not running; refusing to continue without a complete backup"
  fi

  log INFO "Creating ${name} PostgreSQL backup ${final}"
  # Variables are intentionally expanded inside the PostgreSQL container.
  # shellcheck disable=SC2016
  if ! compose exec -T "$service" sh -eu -c \
    'exec pg_dump --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" --format=custom --compress=6 --no-owner --no-acl' \
    > "$tmp"; then
    die "pg_dump failed for ${name}"
  fi

  [[ -s "$tmp" ]] || die "pg_dump produced an empty archive for ${name}"
  if ! compose exec -T "$service" pg_restore --list < "$tmp" > /dev/null; then
    die "backup verification with pg_restore --list failed for ${name}"
  fi

  chmod 0600 "$tmp"
  mv "$tmp" "$final"
  sha256sum "$final" > "${final}.sha256"
  chmod 0600 "${final}.sha256"
}

backup_database postgres sub2api

{
  printf 'created_at=%s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  printf 'label=%s\n' "$label"
  for dump in "$bundle"/*.dump; do
    printf '%s  %s\n' "$(sha256sum "$dump" | awk '{print $1}')" "$(basename "$dump")"
  done
} > "${bundle}/SHA256SUMS"
chmod 0600 "${bundle}/SHA256SUMS"

# Retention is applied only after the verified backup has been committed.
# Remove both the current bundle format and legacy flat dumps.
find "$backup_dir" -mindepth 1 -maxdepth 1 -type d -name 'sub2api-*' \
  -mtime "+${retention_days}" -exec rm -rf {} +
find "$backup_dir" -type f \( -name 'sub2api-*.dump' -o -name 'sub2api-*.dump.sha256' \) \
  -mtime "+${retention_days}" -delete

trap - EXIT
log INFO "PostgreSQL backups verified: ${bundle}"
printf '%s\n' "$bundle"
