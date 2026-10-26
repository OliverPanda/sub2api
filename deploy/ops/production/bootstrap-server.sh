#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  printf 'Run bootstrap-server.sh as root.\n' >&2
  exit 1
fi

SOURCE_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SUB2API_ROOT=${SUB2API_ROOT:-/opt/sub2api}
DEPLOY_USER=${DEPLOY_USER:-sub2api-deploy}
INSTALL_TAILSCALE=${INSTALL_TAILSCALE:-true}
REQUIRE_TAILSCALE=${REQUIRE_TAILSCALE:-true}
CONFIGURE_FIREWALL=${CONFIGURE_FIREWALL:-true}
PUBLIC_DOMAIN=${PUBLIC_DOMAIN:-api.xddapi.org.uk}
ORIGIN_CERT_FILE=${ORIGIN_CERT_FILE:-}
ORIGIN_KEY_FILE=${ORIGIN_KEY_FILE:-}

log() { printf '[bootstrap] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

[[ -f /etc/os-release ]] || die "/etc/os-release is missing"
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}" in
  ubuntu|debian) ;;
  *) die "this bootstrap currently supports Ubuntu and Debian only" ;;
esac

log "Installing base packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl gnupg jq openssl util-linux rsync logrotate ufw iptables

if ! command -v docker >/dev/null 2>&1; then
  log "Installing Docker Engine and Compose plugin"
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL "https://download.docker.com/linux/${ID}/gpg" -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  arch=$(dpkg --print-architecture)
  codename=${VERSION_CODENAME:?VERSION_CODENAME is required}
  cat > /etc/apt/sources.list.d/docker.list <<EOF
deb [arch=${arch} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${ID} ${codename} stable
EOF
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
install -d -m 0755 /etc/docker
daemon_json=/etc/docker/daemon.json
daemon_tmp=$(mktemp)
if [[ -f "$daemon_json" ]]; then
  jq '. + {"ip6tables": true}' "$daemon_json" > "$daemon_tmp"
else
  jq -n '{"ip6tables": true}' > "$daemon_tmp"
fi
if [[ ! -f "$daemon_json" ]] || ! cmp -s "$daemon_tmp" "$daemon_json"; then
  install -m 0644 "$daemon_tmp" "$daemon_json"
  daemon_changed=true
else
  daemon_changed=false
fi
rm -f "$daemon_tmp"
systemctl enable --now docker
if [[ "$daemon_changed" == "true" ]]; then
  systemctl restart docker
fi
docker compose version >/dev/null || die "Docker Compose plugin is unavailable"

if ! id "$DEPLOY_USER" >/dev/null 2>&1; then
  log "Creating restricted deployment user ${DEPLOY_USER}"
  useradd --system --create-home --home-dir "$SUB2API_ROOT" --shell /bin/bash --user-group "$DEPLOY_USER"
fi
usermod -aG docker "$DEPLOY_USER"
passwd -l "$DEPLOY_USER" >/dev/null 2>&1 || true

install -d -m 0755 "$SUB2API_ROOT" "$SUB2API_ROOT/bin" "$SUB2API_ROOT/caddy"
chown root:root "$SUB2API_ROOT" "$SUB2API_ROOT/bin" "$SUB2API_ROOT/caddy"
chmod 0755 "$SUB2API_ROOT" "$SUB2API_ROOT/bin" "$SUB2API_ROOT/caddy"
install -d -m 0750 -o "$DEPLOY_USER" -g "$DEPLOY_USER" \
  "$SUB2API_ROOT/.ssh" \
  "$SUB2API_ROOT/.docker" \
  "$SUB2API_ROOT/backups" \
  "$SUB2API_ROOT/logs" \
  "$SUB2API_ROOT/state" \
  "$SUB2API_ROOT/releases"
# Runtime volumes are initialized once. A rerun must not recursively chown
# PostgreSQL, Redis, or Caddy data back to the deployment account. Legacy
# Bridge/Dujiao directories are deliberately left untouched for rollback.
for runtime_dir in data postgres_data redis_data caddy_data caddy_config; do
  if [[ ! -d "$SUB2API_ROOT/$runtime_dir" ]]; then
    install -d -m 0750 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$SUB2API_ROOT/$runtime_dir"
  fi
done

install -m 0644 "$SOURCE_DIR/docker-compose.yml" "$SUB2API_ROOT/compose.yml"
install -m 0644 "$SOURCE_DIR/Caddyfile" "$SUB2API_ROOT/Caddyfile"
install -m 0644 "$SOURCE_DIR/.env.example" "$SUB2API_ROOT/.env.example"
for script in "$SOURCE_DIR"/scripts/*.sh; do
  install -m 0755 "$script" "$SUB2API_ROOT/bin/$(basename "$script")"
done

if [[ ! -f "$SUB2API_ROOT/caddy/cloudflare-trusted-proxies.caddy" ]]; then
  install -m 0644 "$SOURCE_DIR/caddy/cloudflare-trusted-proxies.caddy" "$SUB2API_ROOT/caddy/cloudflare-trusted-proxies.caddy"
fi
if ! "$SUB2API_ROOT/bin/refresh-cloudflare-trusted-proxies.sh" "$SUB2API_ROOT/caddy/cloudflare-trusted-proxies.caddy"; then
  log "WARNING: could not refresh Cloudflare ranges; the committed fallback snapshot remains in use"
fi

env_file="$SUB2API_ROOT/.env"
if [[ ! -f "$env_file" ]]; then
  install -m 0600 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$SOURCE_DIR/.env.example" "$env_file"
  log "Created ${env_file} from the production template"
fi

env_get() {
  awk -v key="$1" 'index($0, key "=") == 1 { sub(/^[^=]*=/, ""); sub(/\r$/, ""); print; exit }' "$env_file"
}

set_env() {
  local key=$1 value=$2 tmp
  tmp=$(mktemp)
  awk -v key="$key" -v value="$value" '
    BEGIN { done = 0 }
    index($0, key "=") == 1 { print key "=" value; done = 1; next }
    { print }
    END { if (!done) print key "=" value }
  ' "$env_file" > "$tmp"
  cat "$tmp" > "$env_file"
  rm -f "$tmp"
}

ensure_secret() {
  local key=$1 value
  value=$(env_get "$key")
  if [[ -z "$value" ]]; then
    value=$(openssl rand -hex 32)
    set_env "$key" "$value"
    log "Generated ${key}"
  fi
}

set_env PUBLIC_DOMAIN "$PUBLIC_DOMAIN"
set_env SERVER_FRONTEND_URL "https://${PUBLIC_DOMAIN}"
set_env SERVER_TRUSTED_PROXIES "172.30.0.10/32"
set_env SECURITY_TRUST_FORWARDED_IP_FOR_API_KEY_ACL "false"
ensure_secret POSTGRES_PASSWORD
ensure_secret REDIS_PASSWORD
ensure_secret JWT_SECRET
ensure_secret TOTP_ENCRYPTION_KEY

admin_password=$(env_get ADMIN_PASSWORD)
if [[ -z "$admin_password" ]]; then
  admin_password=$(openssl rand -hex 24)
  set_env ADMIN_PASSWORD "$admin_password"
  log "Generated ADMIN_PASSWORD"
fi
admin_email=$(env_get ADMIN_EMAIL)
credentials_file="/root/xddapi-initial-credentials.txt"
if [[ ! -e "$credentials_file" ]]; then
  (
    umask 077
    printf 'SUB2API_ADMIN_EMAIL=%s\n' "$admin_email"
    printf 'SUB2API_ADMIN_PASSWORD=%s\n' "$admin_password"
  ) > "$credentials_file"
  chmod 0600 "$credentials_file"
  log "Wrote first-bootstrap administrator credentials to ${credentials_file}"
else
  log "Keeping existing ${credentials_file}; bootstrap never overwrites credentials"
fi

if [[ -n "${DEPLOY_PUBLIC_KEY:-}" ]]; then
  [[ "$DEPLOY_PUBLIC_KEY" != *$'\n'* && "$DEPLOY_PUBLIC_KEY" != *$'\r'* ]] || die "DEPLOY_PUBLIC_KEY must be exactly one line"
  [[ "$DEPLOY_PUBLIC_KEY" =~ ^(ssh-ed25519|ssh-rsa)[[:space:]][A-Za-z0-9+/]+={0,3}([[:space:]][^[:cntrl:]]+)?$ ]] || die "DEPLOY_PUBLIC_KEY must be a valid OpenSSH public key"
  authorized_keys="$SUB2API_ROOT/.ssh/authorized_keys"
  touch "$authorized_keys"
  if ! grep -Fq "$DEPLOY_PUBLIC_KEY" "$authorized_keys"; then
    printf 'restrict,command="%s" %s\n' "$SUB2API_ROOT/bin/deploy-release" "$DEPLOY_PUBLIC_KEY" >> "$authorized_keys"
  fi
  chown "$DEPLOY_USER:$DEPLOY_USER" "$authorized_keys"
  chmod 0600 "$authorized_keys"
else
  log "WARNING: DEPLOY_PUBLIC_KEY was not provided; install the GitHub Actions public key manually"
fi

chown "$DEPLOY_USER:$DEPLOY_USER" "$env_file"
chmod 0600 "$env_file"
chown -R "$DEPLOY_USER:$DEPLOY_USER" \
  "$SUB2API_ROOT/.ssh" \
  "$SUB2API_ROOT/.docker" \
  "$SUB2API_ROOT/backups" \
  "$SUB2API_ROOT/logs" \
  "$SUB2API_ROOT/state" \
  "$SUB2API_ROOT/releases"

if [[ -n "${GHCR_TOKEN:-}" ]]; then
  [[ -n "${GHCR_USERNAME:-}" ]] || die "GHCR_USERNAME is required when GHCR_TOKEN is provided"
  log "Logging ${DEPLOY_USER} in to GHCR"
  printf '%s' "$GHCR_TOKEN" | runuser -u "$DEPLOY_USER" -- env \
    HOME="$SUB2API_ROOT" DOCKER_CONFIG="$SUB2API_ROOT/.docker" \
    docker login ghcr.io --username "$GHCR_USERNAME" --password-stdin >/dev/null
else
  log "WARNING: GHCR credentials were not provided; make the package public or log in as ${DEPLOY_USER}"
fi

cat > /etc/logrotate.d/sub2api-deploy <<EOF
${SUB2API_ROOT}/logs/*.log {
    daily
    rotate 14
    compress
    missingok
    notifempty
    copytruncate
    su ${DEPLOY_USER} ${DEPLOY_USER}
}
EOF

if [[ "$INSTALL_TAILSCALE" == "true" ]] && ! command -v tailscale >/dev/null 2>&1; then
  log "Installing Tailscale"
  curl -fsSL https://tailscale.com/install.sh | sh
fi

if command -v tailscale >/dev/null 2>&1 && ! tailscale status >/dev/null 2>&1; then
  if [[ -n "${TAILSCALE_AUTH_KEY:-}" ]]; then
    log "Connecting Tailscale as ${TAILSCALE_TAGS:-tag:sub2api-prod}"
    tailscale up \
      --auth-key="$TAILSCALE_AUTH_KEY" \
      --advertise-tags="${TAILSCALE_TAGS:-tag:sub2api-prod}" \
      --hostname="${TAILSCALE_HOSTNAME:-$(hostname -s)}" \
      --ssh=false
  fi
fi

if [[ -n "$ORIGIN_CERT_FILE" || -n "$ORIGIN_KEY_FILE" ]]; then
  [[ -n "$ORIGIN_CERT_FILE" && -n "$ORIGIN_KEY_FILE" ]] || die "ORIGIN_CERT_FILE and ORIGIN_KEY_FILE must be provided together"
  [[ -r "$ORIGIN_CERT_FILE" && -r "$ORIGIN_KEY_FILE" ]] || die "Origin certificate or key file is not readable"
  log "Installing Cloudflare Origin CA certificate"
  install -m 0644 -o root -g root "$ORIGIN_CERT_FILE" "$SUB2API_ROOT/caddy/origin.pem"
  install -m 0600 -o root -g root "$ORIGIN_KEY_FILE" "$SUB2API_ROOT/caddy/origin.key"
fi

if [[ ! -s "$SUB2API_ROOT/caddy/origin.pem" || ! -s "$SUB2API_ROOT/caddy/origin.key" ]]; then
  if [[ "${ALLOW_MISSING_ORIGIN_CERT:-false}" == "true" ]]; then
    log "WARNING: Cloudflare Origin CA certificate files are missing:"
    log "  ${SUB2API_ROOT}/caddy/origin.pem"
    log "  ${SUB2API_ROOT}/caddy/origin.key"
  else
    die "Cloudflare Origin CA certificate is missing; set ORIGIN_CERT_FILE and ORIGIN_KEY_FILE, then rerun bootstrap"
  fi
fi

if [[ -s "$SUB2API_ROOT/caddy/origin.pem" && -s "$SUB2API_ROOT/caddy/origin.key" ]]; then
  openssl x509 -in "$SUB2API_ROOT/caddy/origin.pem" -noout >/dev/null || die "origin.pem is not a valid X.509 certificate"
  openssl pkey -in "$SUB2API_ROOT/caddy/origin.key" -noout >/dev/null || die "origin.key is not a valid private key"
  openssl x509 -in "$SUB2API_ROOT/caddy/origin.pem" -noout -checkend 86400 >/dev/null || die "Cloudflare Origin CA certificate expires within 24 hours"
  cert_pub=$(openssl x509 -in "$SUB2API_ROOT/caddy/origin.pem" -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum | awk '{print $1}')
  key_pub=$(openssl pkey -in "$SUB2API_ROOT/caddy/origin.key" -pubout -outform DER | sha256sum | awk '{print $1}')
  [[ "$cert_pub" == "$key_pub" ]] || die "Cloudflare Origin CA certificate and private key do not match"
  openssl x509 -in "$SUB2API_ROOT/caddy/origin.pem" -noout -checkhost "$PUBLIC_DOMAIN" >/dev/null \
    || die "Cloudflare Origin CA certificate does not cover ${PUBLIC_DOMAIN}"
  chmod 0644 "$SUB2API_ROOT/caddy/origin.pem"
  chmod 0600 "$SUB2API_ROOT/caddy/origin.key"
fi

if ! tailscale status >/dev/null 2>&1; then
  if [[ "$REQUIRE_TAILSCALE" == "true" ]]; then
    die "Tailscale is not connected; run 'tailscale up' or provide TAILSCALE_AUTH_KEY"
  fi
  log "WARNING: Tailscale is not connected; GitHub Actions will not be able to reach this server"
fi

tailscale_ready=false
if tailscale status >/dev/null 2>&1; then
  tailscale_ready=true
fi

if [[ "$CONFIGURE_FIREWALL" == "true" && "$tailscale_ready" == "true" ]]; then
  log "Configuring UFW and Docker firewall rules"
  "${SUB2API_ROOT}/bin/configure-firewall.sh"

  cat > /etc/systemd/system/sub2api-firewall.service <<EOF
[Unit]
Description=Restrict Sub2API Docker port 443 to Cloudflare
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${SUB2API_ROOT}/bin/configure-firewall.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

  cat > /etc/systemd/system/sub2api-cloudflare-ranges.service <<EOF
[Unit]
Description=Refresh Cloudflare trusted proxies for Sub2API
Requires=docker.service
After=docker.service network-online.target sub2api-firewall.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${SUB2API_ROOT}/bin/refresh-cloudflare-ranges.sh
EOF

  cat > /etc/systemd/system/sub2api-cloudflare-ranges.timer <<'EOF'
[Unit]
Description=Weekly Cloudflare range refresh for Sub2API

[Timer]
OnCalendar=weekly
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
EOF

  systemctl daemon-reload
  systemctl enable --now sub2api-firewall.service
  systemctl enable --now sub2api-cloudflare-ranges.timer
elif [[ "$CONFIGURE_FIREWALL" == "true" ]]; then
  log "WARNING: firewall setup skipped because Tailscale is not connected"
else
  log "WARNING: firewall automation disabled by CONFIGURE_FIREWALL=false"
fi

log "Bootstrap complete"
log "Initial admin credentials: ${credentials_file} (delete after saving)"
log "Next: sign in to Sub2API and configure a native payment provider under Settings > Payment"
log "Legacy Bridge/Dujiao containers and data are not modified by bootstrap"
