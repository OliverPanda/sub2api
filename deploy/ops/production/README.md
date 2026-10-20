# Sub2API 生产环境：零按钮自动发布

本文对应 `api.xddapi.org.uk` 的单机生产部署。完成一次初始化和 GitHub 配置后，后续上线只需要向 `main` 打一个版本 tag 并推送：

```bash
git tag -a v0.2.16 -m "release v0.2.16"
git push origin v0.2.16
```

不需要打开 GitHub 页面点击 Deploy，不需要登录服务器，也不需要人工审批。

## 1. 最终流程

```text
合并 main
  -> 推送 vMAJOR.MINOR.PATCH tag
  -> Release 工作流构建镜像并发布 GitHub Release
  -> Deploy Production 工作流自动触发
  -> 从 GitHub latest release 获取正式 tag
  -> 校验 tag commit 与 Release 工作流 SHA 一致
  -> 校验 tag commit 已包含在默认分支 main
  -> 解析 GHCR 镜像 digest（不使用 latest）
  -> 自动维护 Cloudflare DNS / Full (strict) / Always HTTPS
  -> GitHub Runner 加入 Tailscale tag:ci
  -> 通过 Tailscale SSH 进入受限强制命令
  -> 备份并验签 PostgreSQL dump
  -> 拉取固定 digest 镜像并校验 OCI version / revision
  -> 重建 Sub2API 容器
  -> 容器健康检查 + Caddy 检查 + 公网冒烟测试
  -> 失败自动回滚上一个应用镜像
  -> Telegram 通知
```

入口链路：

```text
Internet
  -> Cloudflare Proxy（橙色云）
  -> 源站 443（只允许 Cloudflare 网段）
  -> Caddy（Cloudflare Origin CA）
  -> Sub2API:8080（仅 Docker 内部网络）
  -> PostgreSQL / Redis（仅内部网络）
```

关键安全边界：

- 只部署 GitHub 的 latest stable release，迟到或非 latest 的 Release 工作流不会覆盖生产。
- 公网只开放 `443`，且由 UFW + Docker `DOCKER-USER` 双重限制为 Cloudflare 来源。
- `DOCKER-USER` 只拦截目标为本机地址的 `443`，不会阻断容器主动访问第三方 HTTPS。
- PostgreSQL、Redis 和 Sub2API 都不发布宿主机端口。
- SSH `22` 只允许从 `tailscale0` 进入；GitHub Actions 通过 Tailscale OAuth 加入 `tag:ci`。
- 部署 SSH key 只能执行 `/opt/sub2api/bin/deploy-release`，不能开 shell、不能转发端口。
- 生产镜像固定为 `ghcr.io/...@sha256:...`，Tag 被覆盖也不会影响已解析的发布。
- 数据库采用 forward-only 迁移。自动回滚只回滚应用镜像，不自动恢复数据库。
- 首次发布也会先创建并验证 PostgreSQL dump，不依赖宿主机数据目录权限判断备份时机。

## 2. 当前环境参数

| 项目 | 值 |
| --- | --- |
| Zone | `xddapi.org.uk` |
| 生产域名 | `api.xddapi.org.uk` |
| 服务器 IPv4 | `149.104.31.126` |
| 服务器 IPv6 | `2001:df1:7880:2::3a7` |
| 镜像仓库 | `ghcr.io/oliverpanda/sub2api` |
| 部署用户 | `sub2api-deploy` |
| 安装目录 | `/opt/sub2api` |

## 3. 一次性配置总览

代码侧已经落地，但真实上线前仍必须完成以下一次性操作：

1. Cloudflare 创建 Origin CA 证书，并保存 `origin.pem`、`origin.key`。
2. Cloudflare 创建 API Token：Zone Read、DNS Edit、Zone Settings Edit。
3. Tailscale 创建 ACL tag、服务器 auth key、GitHub Actions OAuth client。
4. 本地生成专用部署 SSH key。
5. 在服务器执行 `bootstrap-server.sh`。
6. 在 GitHub 仓库配置 Secrets，不创建人工审批 Environment。
7. 推送第一个正式 tag。

> 当前执行环境没有服务器登录凭据，所以无法代你完成服务器上的第 5 步。除这一步和云平台后台配置外，发布链路本身已经实现为全自动。

## 4. Cloudflare 一次性配置

### 4.1 创建 Origin CA 证书

Cloudflare Dashboard：

```text
xddapi.org.uk
  -> SSL/TLS
  -> Origin Server
  -> Create Certificate
```

建议：

- 私钥类型：ECDSA P-256 或 RSA 2048。
- Hostnames：`api.xddapi.org.uk`。
- 有效期：15 years。
- 保存为本地文件：`origin.pem` 和 `origin.key`。
- 私钥只能放在可信机器上，禁止提交到 Git 或放进聊天记录。

Caddy 使用该证书作为 Cloudflare 与源站之间的 HTTPS 证书。浏览器不直连源站，所以该证书不需要被普通客户端信任。

### 4.2 创建 API Token

Cloudflare Dashboard：

```text
My Profile
  -> API Tokens
  -> Create Token
  -> Custom token
```

至少需要：

- `Zone Read`
- `DNS Edit`
- `Zone Settings Edit`

Zone Resources 只授权 `xddapi.org.uk`。同时记录 Zone ID：

```text
xddapi.org.uk -> Overview -> Zone ID
```

自动发布会维护以下设置：

- `A api.xddapi.org.uk -> 149.104.31.126`，必须 Proxied。
- `AAAA api.xddapi.org.uk -> 2001:df1:7880:2::3a7`，必须 Proxied。
- SSL/TLS mode：`Full (strict)`。
- Always Use HTTPS：开启。

注意：`ssl=strict` 和 `always_use_https=on` 是 Zone 级设置，会影响该 Zone 下其他主机名。若 `xddapi.org.uk` 还存在其他记录，请确认它们也有有效的 HTTPS 源站。

## 5. Tailscale 一次性配置

### 5.1 ACL

Tailscale Admin Console 的 ACL 至少包含：

```yaml
tagOwners:
  tag:ci:
    - autogroup:admin
  tag:sub2api-prod:
    - autogroup:admin

grants:
  - src: tag:ci
    dst: tag:sub2api-prod
    ip:
      - tcp:22
```

### 5.2 服务器 auth key

创建带 `tag:sub2api-prod` 的 auth key，并确保 Key 的 tag 与 ACL 一致。初始化时通过 `TAILSCALE_AUTH_KEY` 传给脚本。

建议固定 MagicDNS 主机名：

```text
sub2api-prod
```

完整 MagicDNS 名称通常形如：

```text
sub2api-prod.<your-tailnet>.ts.net
```

`PROD_SSH_HOST` 使用 MagicDNS 短名或完整名称，不要使用公网 IP。

### 5.3 GitHub Actions OAuth client

在 Tailscale Admin Console 创建 OAuth client：

- 允许创建 `tag:ci` 节点。
- Scope 至少包含 auth keys 所需权限。
- 保存 `TS_OAUTH_CLIENT_ID` 和 `TS_OAUTH_SECRET`。

工作流使用官方 `tailscale/github-action` 临时加入 tailnet，任务结束时节点退出。

## 6. 专用 SSH Key 与 known_hosts

在本地生成只用于生产的 key：

```bash
ssh-keygen -t ed25519 -a 100 \
  -C "github-actions-sub2api" \
  -f ./sub2api-prod-deploy
```

得到：

```text
sub2api-prod-deploy       # 私钥，放入 GitHub Secret PROD_SSH_KEY
sub2api-prod-deploy.pub   # 公钥，初始化服务器时传入 DEPLOY_PUBLIC_KEY
```

服务器启动后，从一台已经加入 tailnet 的可信机器获取 host key：

```bash
ssh-keyscan -t ed25519 sub2api-prod 2>/dev/null
```

并在服务器上核对 fingerprint：

```bash
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

确认 fingerprint 一致后，将 `ssh-keyscan` 的输出原文放入 GitHub Secret `PROD_SSH_KNOWN_HOSTS`。不要在 GitHub 工作流运行时临时扫描并信任 host key。

## 7. 初始化服务器

首次通过服务器控制台或公网 SSH 登录，然后在服务器上取得本仓库代码：

```bash
git clone https://github.com/OliverPanda/sub2api.git
cd sub2api
```

### 7.1 执行 bootstrap

把 `origin.pem`、`origin.key` 上传到服务器，例如 `/root/origin.pem`、`/root/origin.key`，然后执行：

```bash
DEPLOY_PUBLIC_KEY="$(cat /root/sub2api-prod-deploy.pub)" \
ORIGIN_CERT_FILE=/root/origin.pem \
ORIGIN_KEY_FILE=/root/origin.key \
TAILSCALE_AUTH_KEY='tskey-auth-...' \
TAILSCALE_HOSTNAME=sub2api-prod \
TAILSCALE_TAGS=tag:sub2api-prod \
bash deploy/ops/production/bootstrap-server.sh
```

如果 GHCR package 是 Private，额外传入：

```bash
GHCR_USERNAME=OliverPanda \
GHCR_TOKEN='github_pat_...' \
```

脚本会自动：

- 安装 Docker Engine、Compose、UFW、Tailscale 等。
- 开启 Docker IPv6 iptables。
- 创建受限用户 `sub2api-deploy`。
- 生成 `.env` 中的 PostgreSQL、Redis、JWT、TOTP、管理员密码。
- 安装并校验证书与私钥匹配。
- 写入强制命令 SSH 公钥。
- 加入 Tailscale。
- 配置 UFW、Docker Cloudflare 白名单和每周网段刷新 timer。
- 安装 `/opt/sub2api/bin/deploy-release` 等可信部署工具。
- 重复执行时保留已有密钥；运行时数据目录只在首次创建，不会递归 chown PostgreSQL、Redis、Caddy 数据。

初始管理员密码保存在：

```text
/root/sub2api-initial-credentials.txt
```

保存到密码管理器后删除该文件。

### 7.2 初始化后检查

```bash
tailscale status
tailscale ip -4
ufw status verbose
iptables -S DOCKER-USER
ip6tables -S DOCKER-USER
systemctl status sub2api-firewall.service --no-pager
systemctl list-timers sub2api-cloudflare-ranges.timer
```

此时应满足：

- `22/tcp` 只允许 `tailscale0`。
- `443/tcp` 的 UFW 来源只有 Cloudflare CIDR。
- `DOCKER-USER` 中有 `SUB2API-CLOUDFLARE` 跳转链。
- 公网 `22/443` 不应对任意来源开放。

完成 Tailscale 验证后，建议同时关闭云厂商安全组中的公网 SSH；服务器主机层的 UFW 已经是第二道保护。

### 7.3 可选项

如果不希望脚本自动配置防火墙，可在首次执行时设置：

```bash
CONFIGURE_FIREWALL=false
```

不建议这样做。若 Tailscale 尚未调通，不要启用防火墙，否则可能失去 SSH 入口。

## 8. GitHub 仓库配置

进入：

```text
Settings -> Secrets and variables -> Actions
```

### 8.1 Required Secrets

| Secret | 内容 |
| --- | --- |
| `PROD_SSH_HOST` | `sub2api-prod` 或 Tailscale MagicDNS 全名 |
| `PROD_SSH_USER` | `sub2api-deploy` |
| `PROD_SSH_KEY` | `sub2api-prod-deploy` 私钥全文 |
| `PROD_SSH_KNOWN_HOSTS` | 人工核对过的服务器 host key |
| `TS_OAUTH_CLIENT_ID` | Tailscale OAuth client ID |
| `TS_OAUTH_SECRET` | Tailscale OAuth client secret |
| `CLOUDFLARE_ZONE_ID` | `xddapi.org.uk` 的 Zone ID |
| `CLOUDFLARE_API_TOKEN` | 具备 DNS/Zone Settings 权限的 Token |
| `TELEGRAM_BOT_TOKEN` | Telegram Bot Token |
| `TELEGRAM_CHAT_ID` | 接收通知的 Chat ID |

### 8.2 Optional Secret

| Secret | 默认值 |
| --- | --- |
| `PROD_SSH_PORT` | `22` |

### 8.3 Optional Repository Variables

| Variable | 默认值 |
| --- | --- |
| `CLOUDFLARE_DNS_RECORD_NAME` | `api.xddapi.org.uk` |
| `ORIGIN_IPV4` | `149.104.31.126` |
| `ORIGIN_IPV6` | `2001:df1:7880:2::3a7` |

不要在 GitHub 创建需要人工审批的 `production` Environment。当前工作流刻意没有 Environment gate，否则就不是零按钮发布。

## 9. 第一次正式发布

确认代码已合并到 `main`，CI 通过，然后：

```bash
git switch main
git pull --ff-only
git tag -a v0.2.16 -m "first automated production release"
git push origin v0.2.16
```

随后观察两个工作流：

1. `Release`
2. `Deploy Production`

`Deploy Production` 成功后，Telegram 会收到：

- 版本号
- 固定镜像 digest
- GitHub Actions 运行链接
- 结果状态

首次发布失败时没有上一版镜像可回滚；服务器会保留日志并清除失败发布的 `release.env`，避免下一次把失败版本误认为“上一版”。

## 10. 后续发布

每次上线只需要：

```bash
git switch main
git pull --ff-only
git tag -a v0.2.17 -m "release v0.2.17"
git push origin v0.2.17
```

版本必须是：

```text
vMAJOR.MINOR.PATCH
```

服务器端会拒绝低于当前版本的 tag，防止旧 Tag 的迟到工作流把生产降级。需要主动回滚时使用单独的 rollback 命令。

建议：

- Tag 一经发布不要移动或复用。
- 发布前确保目标 commit 已在 `main` 且 CI 通过。
- 同一时间只发布会串行执行，`concurrency: sub2api-production` 会防止并发。
- 如果需要发布说明，把说明写在 annotated tag 的正文中。

## 11. 自动失败回滚

部署脚本在以下阶段失败时自动回滚：

- 新应用容器不健康。
- Caddy unhealthy。
- 公网 `/health` 或 `/setup/status` 冒烟测试失败。

自动回滚行为：

1. 恢复 `/opt/sub2api/state/release.env.previous`。
2. 拉取上一版固定 digest。
3. 重建 Sub2API 容器。
4. 启动 Caddy 并等待其健康检查通过。
5. 再做应用容器健康检查和公网冒烟测试。
6. 记录 `releases.log`。
7. 不恢复数据库。

如果数据库迁移与新应用不兼容，应用镜像自动回滚可能仍无法工作。此时必须人工恢复数据库，不能让脚本擅自覆盖生产数据。

## 12. 手工操作

以下命令在服务器控制台或 Tailscale SSH 中执行。GitHub Actions 使用的部署 key 被强制命令限制，不能执行任意 shell。

### 12.1 查看状态

```bash
cd /opt/sub2api
docker compose --env-file .env --env-file state/release.env -f compose.yml ps
cat state/release.env
tail -n 100 releases.log
tail -n 200 logs/deploy.log
```

### 12.2 手工应用回滚

```bash
sudo -u sub2api-deploy /opt/sub2api/bin/deploy-release rollback
```

该命令会先做一次数据库备份，再应用回滚上一个应用镜像。

### 12.3 手工备份

```bash
sudo -u sub2api-deploy /opt/sub2api/bin/backup-postgres.sh manual
```

备份验证通过后才会落盘：

```text
/opt/sub2api/backups/sub2api-<UTC>-<label>-<pid>.dump
/opt/sub2api/backups/...sha256
```

### 12.4 数据库恢复：必须人工

数据库恢复会覆盖业务数据，默认不在自动发布流程内。只在确认需要时执行：

```bash
cd /opt/sub2api
docker compose --env-file .env --env-file state/release.env -f compose.yml stop sub2api
docker compose --env-file .env --env-file state/release.env -f compose.yml exec -T postgres \
  sh -eu -c 'pg_restore --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" \
  --clean --if-exists --no-owner --no-acl' < backups/<file>.dump
docker compose --env-file .env --env-file state/release.env -f compose.yml start sub2api
```

执行前应再次备份当前数据库，并确认 dump 文件校验和。恢复完成后观察健康检查和业务验证。

## 13. Cloudflare 网段刷新

初始化脚本会安装：

```text
sub2api-firewall.service
sub2api-cloudflare-ranges.timer
```

timer 每周刷新：

- Caddy `trusted_proxies` 列表。
- UFW 中 Cloudflare 来源规则。
- Docker `DOCKER-USER` 链。

手工立即刷新：

```bash
sudo systemctl start sub2api-cloudflare-ranges.service
sudo journalctl -u sub2api-cloudflare-ranges.service -n 100 --no-pager
```

只有网段实际变化时才重启 Caddy，避免无意义的中断。

## 14. 排查

### 工作流没有触发部署

检查：

- `Release` 是否成功完成。
- Tag 是否严格符合 `vX.Y.Z`。
- 该 Release 是否为 GitHub `latest release`。
- Workflow 文件是否已合并到默认分支 `main`。

### GHCR 拉取 401/403

- 最省事的做法是把 `sub2api` package 改为 Public。
- Private package 必须在 bootstrap 时提供长期有效的 `GHCR_USERNAME` 和 `GHCR_TOKEN`。
- PAT 过期后自动发布会失败，需要更新服务器上的 Docker credential。

### Tailscale SSH 失败

- `PROD_SSH_HOST` 是否是 MagicDNS 或 tailnet IP。
- OAuth client 是否允许 `tag:ci`。
- ACL 是否允许 `tag:ci -> tag:sub2api-prod tcp:22`。
- `PROD_SSH_KNOWN_HOSTS` 的主机名是否与 `PROD_SSH_HOST` 完全一致。

### Cloudflare 521/522/523/525/526

- DNS 是否 Proxied。
- Cloudflare SSL/TLS 是否为 Full (strict)。
- 源站 443 防火墙是否允许当前 Cloudflare 网段。
- `origin.pem` 是否覆盖 `api.xddapi.org.uk`。
- Caddy 是否运行：`docker compose ... ps caddy`。

### 公网冒烟测试失败但容器健康

- 检查 Cloudflare DNS 是否已生效。
- 检查 `ufw status verbose`。
- 检查 `iptables -S DOCKER-USER`。
- 检查 Caddy 日志：
  ```bash
  docker compose --env-file .env --env-file state/release.env -f compose.yml logs --tail=200 caddy
  ```

## 15. 运维注意事项

- `sub2api-deploy` 在 `docker` 组中，因此本质上具有较高的主机控制能力；安全边界主要来自受限 SSH key 和 root-owned 部署脚本。
- `/opt/sub2api/bin`、`compose.yml`、`Caddyfile` 由 root 管理，部署用户不能修改。
- 生产环境 `.env`、Origin 私钥、Docker credential 都不得提交。
- 自动回滚不是数据库时间机器。
- Cloudflare `ssl=strict` 是 Zone 级配置，修改前应确认整个 Zone。
- 安全组/云防火墙也要限制入站端口；主机 UFW 不能替代云厂商安全组。
- 如果重新执行 bootstrap，脚本会保留已有 `.env` 密钥，但会重新安装 root-owned 部署工具和 systemd 单元。

## 16. 文件说明

| 文件 | 作用 |
| --- | --- |
| `.github/workflows/deploy-production.yml` | 自动部署触发器、tag/ digest / Cloudflare / Tailscale / SSH 编排 |
| `bootstrap-server.sh` | 服务器一次性初始化 |
| `docker-compose.yml` | 单机生产 Compose |
| `Caddyfile` | Cloudflare Origin TLS 与反向代理 |
| `caddy/cloudflare-trusted-proxies.caddy` | Caddy 可信代理网段快照 |
| `scripts/deploy-release.sh` | 固定 digest 部署、健康检查、备份、回滚 |
| `scripts/backup-postgres.sh` | PostgreSQL 自定义格式备份和校验 |
| `scripts/wait-health.sh` | 应用容器健康等待 |
| `scripts/smoke-test.sh` | 公网健康与 setup 状态测试 |
| `scripts/sync-cloudflare-dns.sh` | DNS、SSL、Always HTTPS 自动维护 |
| `scripts/configure-firewall.sh` | UFW 与 Docker 层 Cloudflare 来源限制 |
| `scripts/refresh-cloudflare-ranges.sh` | 定期刷新 Caddy / UFW / Docker 网段 |
| `scripts/refresh-cloudflare-trusted-proxies.sh` | 从 Cloudflare 官方地址下载可信代理网段 |
| `scripts/notify-telegram.sh` | 发布结果通知 |
