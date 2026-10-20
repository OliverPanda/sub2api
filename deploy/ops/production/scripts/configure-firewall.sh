#!/usr/bin/env bash
set -Eeuo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  printf 'configure-firewall.sh must run as root.\n' >&2
  exit 1
fi

SUB2API_ROOT=${SUB2API_ROOT:-/opt/sub2api}
ranges_file=${CLOUDFLARE_RANGES_FILE:-${SUB2API_ROOT}/caddy/cloudflare-trusted-proxies.caddy}
allow_public_ssh=${ALLOW_PUBLIC_SSH:-false}

log() { printf '[firewall] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

[[ -r "$ranges_file" ]] || die "missing Cloudflare range file: ${ranges_file}"
for command in ufw iptables ip6tables; do
  command -v "$command" >/dev/null 2>&1 || die "${command} is required"
done

valid_ipv4_cidr() {
  local value=$1 address prefix octet
  local -a octets
  [[ "$value" == */* ]] || return 1
  address=${value%/*}
  prefix=${value#*/}
  [[ "$prefix" =~ ^([0-9]|[12][0-9]|3[0-2])$ ]] || return 1
  IFS=. read -r -a octets <<< "$address"
  [[ ${#octets[@]} -eq 4 ]] || return 1
  for octet in "${octets[@]}"; do
    [[ "$octet" =~ ^[0-9]{1,3}$ ]] || return 1
    (( 10#$octet <= 255 )) || return 1
  done
}

valid_ipv6_cidr() {
  local value=$1 address prefix
  [[ "$value" == *:* && "$value" == */* ]] || return 1
  address=${value%/*}
  prefix=${value#*/}
  [[ "$prefix" =~ ^([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$ ]] || return 1
  [[ "$address" =~ ^[0-9A-Fa-f:]+$ ]] || return 1
}

ipv4_ranges=()
ipv6_ranges=()
while IFS= read -r cidr; do
  [[ -z "$cidr" ]] && continue
  cidr=${cidr//$'\r'/}
  if valid_ipv4_cidr "$cidr"; then
    ipv4_ranges+=("$cidr")
  elif valid_ipv6_cidr "$cidr"; then
    ipv6_ranges+=("$cidr")
  else
    die "invalid Cloudflare CIDR: ${cidr}"
  fi
done < <(awk '$1 == "trusted_proxies" && $2 == "static" { for (i = 3; i <= NF; i++) print $i }' "$ranges_file")

[[ ${#ipv4_ranges[@]} -gt 0 ]] || die "no Cloudflare IPv4 ranges found in ${ranges_file}"
[[ ${#ipv6_ranges[@]} -gt 0 ]] || die "no Cloudflare IPv6 ranges found in ${ranges_file}"

# UFW protects host sockets and the Docker userland proxy. Docker's DNAT path
# bypasses ordinary UFW INPUT rules, so DOCKER-USER rules below are mandatory.
if [[ -f /etc/default/ufw ]]; then
  sed -i 's/^IPV6=.*/IPV6=yes/' /etc/default/ufw
fi
ufw --force default deny incoming >/dev/null
ufw --force default allow outgoing >/dev/null
if [[ "$allow_public_ssh" != "true" ]]; then
  ufw --force delete allow 22/tcp >/dev/null 2>&1 || true
  ufw --force delete allow OpenSSH >/dev/null 2>&1 || true
fi
ufw allow in on tailscale0 to any port 22 proto tcp comment 'Sub2API SSH over Tailscale' >/dev/null
ufw allow 41641/udp comment 'Tailscale direct connection' >/dev/null
if [[ "$allow_public_ssh" == "true" ]]; then
  ufw allow 22/tcp comment 'Temporary public SSH' >/dev/null
fi
for cidr in "${ipv4_ranges[@]}" "${ipv6_ranges[@]}"; do
  ufw allow proto tcp from "$cidr" to any port 443 comment 'Sub2API Cloudflare origin' >/dev/null
done
ufw --force enable >/dev/null

apply_docker_chain() {
  local iptables_command=$1
  shift
  local -a cidrs=("$@")
  local chain=SUB2API-CLOUDFLARE cidr
  if ! "$iptables_command" -w 5 -S DOCKER-USER >/dev/null 2>&1; then
    log "WARNING: ${iptables_command} DOCKER-USER chain is unavailable; Docker traffic is only protected by UFW"
    return 0
  fi
  "$iptables_command" -w 5 -N "$chain" >/dev/null 2>&1 || true
  "$iptables_command" -w 5 -F "$chain"
  for cidr in "${cidrs[@]}"; do
    "$iptables_command" -w 5 -A "$chain" -s "$cidr" -p tcp --dport 443 -j RETURN
  done
  "$iptables_command" -w 5 -A "$chain" -p tcp --dport 443 -j DROP

  # DOCKER-USER also sees container-originated outbound traffic. Limit the
  # policy to traffic destined for a local address; otherwise API calls from
  # Sub2API to third-party HTTPS endpoints would be dropped as well.
  jump_spec=(-m addrtype --dst-type LOCAL -p tcp --dport 443 -j "$chain")
  while "$iptables_command" -w 5 -C DOCKER-USER -p tcp --dport 443 -j "$chain" >/dev/null 2>&1; do
    "$iptables_command" -w 5 -D DOCKER-USER -p tcp --dport 443 -j "$chain"
  done
  if ! "$iptables_command" -w 5 -C DOCKER-USER "${jump_spec[@]}" >/dev/null 2>&1; then
    "$iptables_command" -w 5 -I DOCKER-USER 1 "${jump_spec[@]}"
  fi
}

apply_docker_chain iptables "${ipv4_ranges[@]}"
apply_docker_chain ip6tables "${ipv6_ranges[@]}"

log "Applied firewall policy: public 443 only from Cloudflare; SSH only over Tailscale"
