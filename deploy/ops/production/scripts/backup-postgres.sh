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

if [[ -z "$(compose ps -q postgres 2>/dev/null || true)" ]]; then
  die "postgres service is not running; refusing to continue without a backup"
fi

stamp=$(date -u +'%Y%m%dT%H%M%SZ')
base="${backup_dir}/sub2api-${stamp}-${label}-$$"
tmp="${base}.dump.tmp"
final="${base}.dump"

cleanup() {
  rm -f "$tmp"
}
trap cleanup EXIT

log INFO "Creating PostgreSQL backup ${final}"
# Variables are intentionally expanded inside the PostgreSQL container.
# shellcheck disable=SC2016
if ! compose exec -T postgres sh -eu -c \
  'exec pg_dump --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" --format=custom --compress=6 --no-owner --no-acl' \
  > "$tmp"; then
  die "pg_dump failed"
fi

[[ -s "$tmp" ]] || die "pg_dump produced an empty archive"
if ! compose exec -T postgres pg_restore --list < "$tmp" > /dev/null; then
  die "backup verification with pg_restore --list failed"
fi

chmod 0600 "$tmp"
mv "$tmp" "$final"
sha256sum "$final" > "${final}.sha256"
chmod 0600 "${final}.sha256"

# Retention is applied only after a verified backup has been committed.
find "$backup_dir" -type f \( -name 'sub2api-*.dump' -o -name 'sub2api-*.dump.sha256' \) \
  -mtime "+${retention_days}" -delete

trap - EXIT
log INFO "PostgreSQL backup verified: ${final}"
printf '%s\n' "$final"
