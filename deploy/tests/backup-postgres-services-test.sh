#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

cat > "$TEMP_DIR/docker" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail
args=" $* "
if [[ "$*" == *"--services" ]]; then
  for service in $MOCK_DB_SERVICES; do printf '%s\n' "$service"; done
elif [[ "$args" == *" -q "* ]]; then
  printf 'container-%s\n' "${!#}"
elif [[ "$args" == *" exec -T "* ]]; then
  service=""
  restore=false
  previous=""
  for argument in "$@"; do
    if [[ "$previous" == "-T" ]]; then service=$argument; fi
    [[ "$argument" == "pg_restore" ]] && restore=true
    previous=$argument
  done
  if [[ "$restore" == true ]]; then
    cat >/dev/null
  else
    printf 'backup-%s\n' "$service"
  fi
else
  printf 'unexpected docker invocation: %s\n' "$*" >&2
  exit 1
fi
MOCK
chmod +x "$TEMP_DIR/docker"

run_backup() {
  local label=$1 services=$2 expected=$3 root="$TEMP_DIR/$1"
  mkdir -p "$root"
  local bundle
  bundle=$(MOCK_DB_SERVICES="$services" SUB2API_ROOT="$root" PATH="$TEMP_DIR:$PATH" \
    bash "$ROOT_DIR/deploy/ops/production/scripts/backup-postgres.sh" "$label")
  for database in $expected; do
    test -s "$bundle/$database.dump"
  done
  test "$(find "$bundle" -maxdepth 1 -name '*.dump' | wc -l | tr -d ' ')" -eq "$(wc -w <<< "$expected" | tr -d ' ')"
}

run_backup single "postgres sub2api playground redis" "sub2api"
run_backup multi "postgres bridge-postgres dujiao-postgres redis" "sub2api bridge dujiao"

echo "PostgreSQL backup service checks passed"
