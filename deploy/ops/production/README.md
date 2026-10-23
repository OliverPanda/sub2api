# XDD API 生产环境：Dujiao 充值 Bridge 与零按钮发布

本文对应 `xddapi.org.uk` 的单机生产部署。代码覆盖 Sub2API、Dujiao 充值 Bridge、固定版本的 Dujiao-Next，以及 GitHub Actions 自动发布链路。

完成一次服务器初始化和 GitHub 配置后，日常上线不再需要打 tag、点 Deploy、登录服务器或人工审批：

```text
push main
  -> CI
  -> Auto Release 自动 patch 版本、提交 VERSION、打 tag、原子 push
  -> Release 构建并发布 sub2api + sub2api-bridge 双镜像
  -> Deploy Production 自动部署
  -> 失败自动回滚应用镜像
  -> Telegram 通知
```

只有应用代码发生变化时才会自动发版。纯文档、纯运维文档提交可以只跑 CI，不产生新版本。

## 1. 生产架构

```text
Internet
  -> Cloudflare Proxy（橙色云）
  -> 源站 443（只允许 Cloudflare 来源）
  -> Caddy（Cloudflare Origin CA）
       ├─ api.xddapi.org.uk/          -> Sub2API
       ├─ api.xddapi.org.uk/shop/     -> Bridge
       ├─ api.xddapi.org.uk/internal/ -> Bridge 回调
       └─ pay.xddapi.org.uk/          -> Dujiao-Next
```

同源 `/shop/` 是刻意设计：Bridge 从 Sub2API 的 `localStorage.auth_token` 读取登录态，不接收 URL 中的 JWT。支付回调经过 HMAC 验签后，Bridge 仍会重新查询 Dujiao 订单，再通过 Sub2API Admin API 幂等入账。

生产服务：

```text
caddy
sub2api
postgres
redis
bridge
bridge-postgres
dujiao-next
dujiao-postgres
dujiao-redis
```

关键安全边界：

- 公网只开放 `443`，主机 UFW 与 Docker `DOCKER-USER` 双重限制为 Cloudflare 来源。
- SSH `22` 只允许从 `tailscale0` 进入；GitHub Actions 通过 Tailscale OAuth 加入 `tag:ci`。
- GitHub Actions 的部署 key 只能执行 `/opt/sub2api/bin/deploy-release`，不能开 shell、转发端口或执行任意命令。
- Sub2API、Bridge、Dujiao、PostgreSQL、Redis 都不发布宿主机端口。
- 生产应用镜像固定为 `@sha256:...`；Dujiao 镜像也固定 digest，不随应用版本漂移。
- 自动回滚只回滚 Sub2API 和 Bridge 镜像，不自动恢复数据库。

## 2. 当前环境参数

| 项目 | 值 |
| --- | --- |
| Cloudflare Zone | `xddapi.org.uk` |
| Sub2API / Bridge | `api.xddapi.org.uk` |
| Dujiao-Next | `pay.xddapi.org.uk` |
| 服务器 IPv4 | `149.104.31.126` |
| 服务器 IPv6 | `2001:df1:7880:2::3a7` |
| GHCR Sub2API | `ghcr.io/oliverpanda/sub2api` |
| GHCR Bridge | `ghcr.io/oliverpanda/sub2api-bridge` |
| 部署用户 | `sub2api-deploy` |
| 安装目录 | `/opt/sub2api` |

## 3. 一次性配置总览

上线前只需要完成一次：

1. Cloudflare 创建同时覆盖 `api.xddapi.org.uk` 和 `pay.xddapi.org.uk` 的 Origin CA 证书。
2. Cloudflare 创建 Zone Read、DNS Edit、Zone Settings Edit 权限的 API Token。
3. Tailscale 配置 ACL、服务器 auth key 和 GitHub Actions OAuth client。
4. 生成专用部署 SSH key。
5. 在服务器执行 `bootstrap-server.sh`。
6. 在 GitHub 配置本文列出的 Secrets；不要给 `production` Environment 加人工审批。
7. 首次部署后配置 Dujiao Channel Client、支付通道、商品、SKU，以及 Sub2API Admin API Key。
8. 跑一笔低金额订单，确认回调、查单、幂等入账和对账。

其中第 5、7、8 步必须由拥有服务器和支付后台权限的人完成。其余步骤配置好后，日常发布是零按钮。

## 4. Cloudflare 一次性配置

### 4.1 Origin CA 证书

Cloudflare Dashboard：

```text
xddapi.org.uk
  -> SSL/TLS
  -> Origin Server
  -> Create Certificate
```

必须同时填写两个 Hostnames：

```text
api.xddapi.org.uk
pay.xddapi.org.uk
```

建议 ECDSA P-256 或 RSA 2048、15 年有效期。保存为：

```text
origin.pem
origin.key
```

私钥不得提交到 Git、聊天或日志。`bootstrap-server.sh` 会校验证书和私钥匹配，并分别检查两个域名的 SAN；缺一个都会拒绝启动。

### 4.2 Cloudflare API Token

```text
My Profile
  -> API Tokens
  -> Create Token
  -> Custom token
```

至少授予：

- Zone Read
- DNS Edit
- Zone Settings Edit

Zone Resources 只授权 `xddapi.org.uk`。记录 Zone Overview 页中的 Zone ID。

自动发布会维护两组记录，A 和 AAAA 都必须 Proxied：

```text
api.xddapi.org.uk -> 149.104.31.126 / 2001:df1:7880:2::3a7
pay.xddapi.org.uk -> 149.104.31.126 / 2001:df1:7880:2::3a7
```

同时设置 Zone 级：

- SSL/TLS mode：`Full (strict)`
- Always Use HTTPS：开启

`ssl=strict` 和 `always_use_https=on` 会影响整个 Zone。若 Zone 里还有其他主机名，必须确认它们也有有效 HTTPS 源站。

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

创建带 `tag:sub2api-prod` 的 auth key。初始化服务器时通过 `TAILSCALE_AUTH_KEY` 传入，主机名建议固定为 `sub2api-prod`。

`PROD_SSH_HOST` 使用 MagicDNS 短名或全名，不要使用公网 IP。

### 5.2 GitHub Actions OAuth client

在 Tailscale Admin Console 创建 OAuth client：

- 允许创建 `tag:ci` 节点。
- 至少允许创建 auth key 所需的 scope。
- 保存 `TS_OAUTH_CLIENT_ID` 和 `TS_OAUTH_SECRET`。

工作流使用 `tailscale/github-action` 建立临时节点，任务结束后退出。

## 6. 部署 SSH key

在可信本地机器生成专用 key：

```bash
ssh-keygen -t ed25519 -a 100 \
  -C "github-actions-sub2api" \
  -f ./sub2api-prod-deploy
```

得到：

```text
sub2api-prod-deploy       # GitHub Secret PROD_SSH_KEY
sub2api-prod-deploy.pub   # 初始化服务器用
```

服务器加入 tailnet 后，从可信机器获取并人工核对 host key：

```bash
ssh-keyscan -t ed25519 sub2api-prod 2>/dev/null
```

在服务器核对 fingerprint：

```bash
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

确认 fingerprint 一致后，把 `ssh-keyscan` 输出原文放入 `PROD_SSH_KNOWN_HOSTS`。不要在 Actions 运行时临时扫描并信任 host key。

## 7. 初始化服务器

首次通过服务器控制台或临时公网 SSH 登录，取得代码：

```bash
git clone https://github.com/OliverPanda/sub2api.git
cd sub2api
```

把以下文件上传到服务器：

```text
/root/origin.pem
/root/origin.key
/root/sub2api-prod-deploy.pub
```

执行：

```bash
DEPLOY_PUBLIC_KEY="$(cat /root/sub2api-prod-deploy.pub)" \
ORIGIN_CERT_FILE=/root/origin.pem \
ORIGIN_KEY_FILE=/root/origin.key \
TAILSCALE_AUTH_KEY='tskey-auth-...' \
TAILSCALE_HOSTNAME=sub2api-prod \
TAILSCALE_TAGS=tag:sub2api-prod \
bash deploy/ops/production/bootstrap-server.sh
```

如果 GHCR package 是 Private，再加：

```bash
GHCR_USERNAME=OliverPanda \
GHCR_TOKEN='github_pat_...' \
```

脚本会：

- 安装 Docker Engine、Compose plugin、UFW、Tailscale 等。
- 把生产 Compose 安装为 `/opt/sub2api/compose.yml`。
- 创建受限用户 `sub2api-deploy`。
- 生成 PostgreSQL、Redis、JWT、TOTP、Bridge、Dujiao 和后台管理员密钥。
- 渲染一次性的 `/opt/sub2api/dujiao/config.yml`。
- 校验证书、私钥和两个域名 SAN。
- 安装受限 SSH 公钥。
- 配置 UFW、Cloudflare 来源限制和每周网段刷新 timer。

初始凭据保存到：

```text
/root/xddapi-initial-credentials.txt
```

包含 Sub2API 管理员、Dujiao 管理员地址/账号密码和 `BRIDGE_ADMIN_TOKEN`。保存到密码管理器后删除该文件。

### 7.1 初始化后检查

```bash
tailscale status
tailscale ip -4
ufw status verbose
iptables -S DOCKER-USER
ip6tables -S DOCKER-USER
systemctl status sub2api-firewall.service --no-pager
systemctl list-timers sub2api-cloudflare-ranges.timer
```

应满足：

- `22/tcp` 只允许 `tailscale0`。
- `443/tcp` 的 UFW 来源只有 Cloudflare CIDR。
- `DOCKER-USER` 中存在 `SUB2API-CLOUDFLARE` 跳转链。
- 公网 `22/443` 不对任意来源开放。

确认 Tailscale SSH 可用后，再关闭云厂商安全组中的公网 SSH。主机 UFW 不能替代云厂商安全组。

## 8. GitHub Secrets

进入仓库：

```text
Settings
  -> Secrets and variables
  -> Actions
```

### 8.1 必需 Secrets

| Secret | 内容 |
| --- | --- |
| `RELEASE_TOKEN` | Fine-grained PAT，目标仓库 Contents: Read and write |
| `CLOUDFLARE_ZONE_ID` | `xddapi.org.uk` Zone ID |
| `CLOUDFLARE_API_TOKEN` | Zone Read、DNS Edit、Zone Settings Edit |
| `TS_OAUTH_CLIENT_ID` | Tailscale OAuth client ID |
| `TS_OAUTH_SECRET` | Tailscale OAuth client secret |
| `PROD_SSH_HOST` | `sub2api-prod` 或 MagicDNS 全名 |
| `PROD_SSH_USER` | `sub2api-deploy` |
| `PROD_SSH_KEY` | `sub2api-prod-deploy` 私钥全文 |
| `PROD_SSH_KNOWN_HOSTS` | 人工核对过的 host key |
| `TELEGRAM_BOT_TOKEN` | Telegram Bot Token |
| `TELEGRAM_CHAT_ID` | 接收发布通知的 Chat ID |

`RELEASE_TOKEN` 用于 Auto Release 向 `main` 提交 `VERSION` 并原子推送 tag，必须是能绕过当前分支保护写入 `main` 的细粒度 PAT；默认 `GITHUB_TOKEN` 不能满足这条跨工作流写回要求。

### 8.2 可选配置

| 名称 | 默认值 |
| --- | --- |
| Secret `PROD_SSH_PORT` | `22` |
| Variable `CLOUDFLARE_DNS_RECORD_NAMES` | `api.xddapi.org.uk pay.xddapi.org.uk` |
| Variable `ORIGIN_IPV4` | `149.104.31.126` |
| Variable `ORIGIN_IPV6` | `2001:df1:7880:2::3a7` |
| Variable `SIMPLE_RELEASE` | 必须为 `false` 或不设置 |

不要设置 `SIMPLE_RELEASE=true`：该模式只发布 x86_64 的 Sub2API 镜像，不满足生产双镜像部署要求。

不要在 GitHub 创建带 Required reviewers 的 `production` Environment。当前工作流刻意没有人工 Environment gate，否则就不是零按钮发布。

## 9. 自动发布链路

### 9.1 日常上线

正常开发只需要：

```bash
git switch main
git pull --ff-only
# 修改代码并提交
git push origin main
```

随后自动发生：

1. `CI` 校验代码。
2. `Auto Release` 在 CI 成功后计算下一个 patch 版本。
3. 自动提交 `backend/cmd/server/VERSION`、创建 annotated tag，并原子推送 `main + tag`。
4. `Release` 构建并发布：
   - `ghcr.io/oliverpanda/sub2api:vX.Y.Z`
   - `ghcr.io/oliverpanda/sub2api-bridge:vX.Y.Z`
5. `Deploy Production` 只接受 GitHub latest stable release，解析两个镜像的不可变 digest。
6. 自动维护两条 Cloudflare DNS 记录和 Zone TLS 设置。
7. GitHub Runner 加入 `tag:ci`，通过 Tailscale SSH 执行受限部署命令。
8. 服务器备份数据库、拉取固定 digest、重建 Sub2API 和 Bridge、检查 Caddy 和公网。
9. 任一阶段失败时，自动恢复上一组应用镜像。
10. Telegram 发送结果。

不需要手工执行 `git tag`、`git push origin <tag>` 或 GitHub Deploy 按钮。

### 9.2 为什么纯文档 push 没发版

`Auto Release` 只检查可能影响运行时产物的路径，例如：

```text
backend/
frontend/
bridge/
Dockerfile.goreleaser
deploy/docker-entrypoint.sh
.goreleaser*.yaml
```

README、FAQ、运营文档等提交不会产生新版本。这能避免无意义的应用发布。

### 9.3 首次上线

首次可以等服务器和 Secrets 配置完成后，再向 `main` 推送一个应用改动，让完整链路自然触发。若代码已经推送且希望使用现有 latest release，也可以只在首次手动运行一次 `Deploy Production` workflow；这一次之后恢复 push `main` 零按钮发布。

### 9.4 发布约束

- 版本严格为 `vMAJOR.MINOR.PATCH`。
- 服务器拒绝低于当前版本的 tag，防止迟到工作流降级。
- 同一时间只允许一个生产部署，`concurrency: sub2api-production` 串行化。
- Tag 不移动、不复用。
- 已发布 tag 不自动删除。
- Dujiao-Next 固定为 digest，不随 Sub2API 应用版本升级。

## 10. 首次配置 Dujiao 和充值

首次部署成功后，按 `docs/DUJIAO_SUB2API_BRIDGE_CN.md` 完成配置。入口为初始凭据中的 Dujiao 管理 URL。

### 10.1 Dujiao Channel Client

创建 Channel Client 时必须使用：

```text
channel_type: telegram_bot
callback_url: https://api.xddapi.org.uk
```

Dujiao 会回调：

```text
https://api.xddapi.org.uk/internal/order-paid
https://api.xddapi.org.uk/internal/order-fulfilled
```

把生成的 Channel Key/Secret 写入 `/opt/sub2api/.env`：

```dotenv
DUJIAO_CHANNEL_KEY=...
DUJIAO_CHANNEL_SECRET=...
```

### 10.2 Dujiao 商品和支付通道

在 Dujiao 后台创建并启用：

1. 实际支付通道，例如 Stripe、Airwallex 或已审核的第三方聚合支付。
2. 充值商品。
3. 商品 SKU，并记录 `product_id`、`sku_id`、币种、价格。

然后在服务器编辑：

```text
/opt/sub2api/bridge/config/skus.json
```

模板是 `{}`，不能直接用于生产。示例：

```json
{
  "cny-10": {
    "display_name": "10 元充值",
    "product_id": 12,
    "sku_id": 34,
    "price": "10.00",
    "credit": "10.00",
    "currency": "CNY",
    "credit_unit": "余额",
    "min_quantity": 1,
    "max_quantity": 10,
    "recommended": false,
    "description": "支付成功后自动到账"
  }
}
```

`price` 必须与 Dujiao SKU 完全相同；`credit` 才是 Sub2API 余额增加量。修改 SKU 后需要重启 Bridge。

### 10.3 Sub2API Admin API Key

在 Sub2API 管理后台创建专用 Admin API Key，仅给 Bridge 使用：

```dotenv
SUB2API_ADMIN_API_KEY=...
```

Bridge 入账调用：

```http
POST /api/v1/admin/redeem-codes/create-and-redeem
x-api-key: <SUB2API_ADMIN_API_KEY>
Idempotency-Key: redeem-<bridge-order-no>
```

充值码使用固定值 `xddshop-<bridge-order-no>`，重复回调不会重复加余额。Admin Key 缺失或入账失败时，订单保留为 `credit_failed` 并延迟重试，不会丢单。

### 10.4 写入配置并重启

以 root 编辑：

```bash
/opt/sub2api/.env
```

至少确认：

```dotenv
DUJIAO_CHANNEL_KEY=...
DUJIAO_CHANNEL_SECRET=...
SUB2API_ADMIN_API_KEY=...
BRIDGE_SUPPORT_URL=https://你的客服地址
BRIDGE_SUPPORT_LABEL=联系客服
```

重启 Bridge：

```bash
cd /opt/sub2api
docker compose --env-file .env --env-file state/release.env -f compose.yml up -d --no-deps --force-recreate bridge
```

检查：

```bash
curl -fsS https://api.xddapi.org.uk/shop/healthz | jq
curl -fsS https://pay.xddapi.org.uk/ >/dev/null
```

期望：

```text
status=ok
payment_configured=true
crediting_configured=true
```

`payment_configured=true` 只表示 Bridge 已有 Dujiao Channel 凭据，不代表支付通道审核通过。实际可用支付方式以 `/shop/api/payment-channels` 和 Dujiao 后台为准。

不要同时启用 Sub2API 原生充值和 Bridge。确定使用 Bridge 后，`/shop/` 是唯一充值入口，否则会出现两套订单、两套对账口径。

## 11. 支付合规路线

香港服务器和境外域名本身不是支付资质。是否能接官方微信/支付宝，取决于商户主体、KYC、业务类目和通道审核。

香港主体的推荐顺序：

1. Airwallex HK
2. Stripe HK
3. WeChat Pay Global / Tenpay Global
4. Antom / Alipay 跨境

大陆支付宝开放平台、大陆微信支付 APIv3 的商户号通常不能直接复用于香港主体。Stripe 可以展示 Alipay/WeChat Pay，但它是 Stripe 聚合收单，不应宣称“微信/支付宝官方直连”。

EasyPay、卡密和第三方聚合支付可以用于过渡，但必须标注为第三方支付，控制额度，保留流水和退款记录，不能冒充官方通道。

详细的资质、费用确认项和技术衔接见：

```text
docs/PAYMENT_HK_CN.md
```

## 12. 上线验收

```text
[ ] api.xddapi.org.uk 可登录 Sub2API
[ ] pay.xddapi.org.uk 可打开 Dujiao
[ ] /shop/ 可读取当前登录用户
[ ] /shop/healthz 返回 status=ok
[ ] payment_configured=true
[ ] crediting_configured=true
[ ] skus.json 不为空，product_id / sku_id / price / currency 正确
[ ] Dujiao 支付方式出现在 /shop/
[ ] 低金额测试订单支付成功
[ ] 回调验签通过，伪造回调和重放回调被拒绝
[ ] 余额只增加一次
[ ] credit_failed 订单可自动重试或人工重试
[ ] 客服入口可打开
[ ] 生产 .env、Origin key、支付密钥未进入 Git
```

## 13. 自动回滚

以下阶段失败时会自动应用回滚：

- 新 Sub2API 或 Bridge 容器不健康。
- Caddy unhealthy。
- 公网健康或 setup 状态冒烟测试失败。

回滚行为：

1. 恢复 `state/release.env.previous`。
2. 拉取上一组固定 digest。
3. 重建 Sub2API 和 Bridge。
4. 重启 Caddy 并等待健康。
5. 再跑公网冒烟测试。
6. 记录 `releases.log`。

数据库迁移是 forward-only。自动回滚不会恢复 PostgreSQL、Bridge 订单或 Dujiao 支付数据。数据库不兼容时必须人工处理。

## 14. 日常运维命令

```bash
cd /opt/sub2api

# 服务状态
docker compose --env-file .env --env-file state/release.env -f compose.yml ps

# 当前发布
cat state/release.env

# 发布历史
tail -n 100 releases.log

# 部署日志
tail -n 200 logs/deploy.log

# Bridge / Dujiao 日志
docker compose --env-file .env --env-file state/release.env -f compose.yml logs --tail=200 bridge
docker compose --env-file .env --env-file state/release.env -f compose.yml logs --tail=200 dujiao-next
```

手工应用回滚：

```bash
sudo -u sub2api-deploy /opt/sub2api/bin/deploy-release rollback
```

手工数据库备份：

```bash
sudo -u sub2api-deploy /opt/sub2api/bin/backup-postgres.sh manual
```

数据库恢复必须人工执行，不能纳入自动发布。

## 15. 故障排查

### 工作流没有发布

- 修改是否包含 `backend/`、`frontend/`、`bridge/` 等应用路径。
- CI 是否成功。
- `RELEASE_TOKEN` 是否有 Contents Read/Write。
- GitHub latest release 是否为新 tag。
- Workflow 文件是否已经存在于默认分支 `main`。

### 部署时提示缺少 `compose.yml`

生产文件安装名是 `/opt/sub2api/compose.yml`。脚本统一使用该文件；不要再改回 `docker-compose.yml`。

### GHCR 401/403

- 最省事：把 `sub2api` 和 `sub2api-bridge` 两个 package 都设为 Public。
- Private package：bootstrap 时提供长期有效的 `GHCR_USERNAME` 和 `GHCR_TOKEN`。
- PAT 过期后更新服务器 Docker credential。

### Tailscale SSH 失败

- `PROD_SSH_HOST` 是否为正确 MagicDNS。
- OAuth client 是否允许 `tag:ci`。
- ACL 是否允许 `tag:ci -> tag:sub2api-prod tcp:22`。
- `PROD_SSH_KNOWN_HOSTS` 的主机名是否与 `PROD_SSH_HOST` 完全一致。

### Cloudflare 521/522/523/525/526

- 两个 DNS 名称是否都是 Proxied。
- SSL/TLS 是否为 Full (strict)。
- 源站 443 是否允许当前 Cloudflare 网段。
- Origin 证书 SAN 是否同时覆盖 `api` 和 `pay`。
- Caddy 是否 healthy。

### 充值页报错

- `PAYMENT_NOT_CONFIGURED`：Channel Key/Secret 没写入或 Bridge 没重启。
- `PRICE_CHANGED`：`skus.json` 和 Dujiao SKU 的价格/币种不一致。
- `PAYMENT_CHANNELS_UNAVAILABLE`：Dujiao Channel Client 或支付通道异常。
- `credit_failed`：保存了支付，但 Sub2API 入账失败；确认 Admin API Key 后重试。
- 回调 `401`：检查 Channel Secret、签名算法、60 秒时间偏差和代理路径。
- 用户已付款但订单取消：先查支付流水，人工审核，不要直接手工加余额。

## 16. 文件说明

| 文件 | 作用 |
| --- | --- |
| `.github/workflows/auto-release.yml` | CI 成功后自动 patch 版本、提交 VERSION、打 tag |
| `.github/workflows/release.yml` | 构建并发布 Sub2API 和 Bridge 双镜像 |
| `.github/workflows/deploy-production.yml` | Cloudflare、Tailscale、固定 digest 生产部署 |
| `bootstrap-server.sh` | 服务器一次性初始化 |
| `docker-compose.yml` | 安装到服务器后的 `compose.yml` |
| `Caddyfile` | 双域名 TLS、Sub2API、Bridge、Dujiao 反向代理 |
| `bridge/config/skus.json` | 服务端充值 SKU 配置 |
| `dujiao/config.yml.example` | Dujiao-Next 渲染模板 |
| `scripts/deploy-release.sh` | 固定 digest 部署、备份、健康检查、回滚 |
| `scripts/sync-cloudflare-dns.sh` | 两条 DNS 记录和 Zone TLS 设置 |
| `scripts/configure-firewall.sh` | UFW 与 Docker Cloudflare 来源限制 |
| `scripts/backup-postgres.sh` | PostgreSQL 备份和校验 |
| `scripts/notify-telegram.sh` | 发布结果通知 |

相关文档：

- `docs/DUJIAO_SUB2API_BRIDGE_CN.md`
- `docs/PAYMENT_INTERACTION_CN.md`
- `docs/PAYMENT_HK_CN.md`
