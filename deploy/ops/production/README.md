# XDD API 生产环境：Sub2API 原生支付与零按钮发布

本文对应 `xddapi.org.uk` 的单机生产部署。

生产支付已统一为 **Sub2API 原生支付**。生产 Compose、Caddy 和 GitHub
发布链路不再包含 Dujiao、Dujiao Bridge、Bridge PostgreSQL 或 Bridge Redis。
仓库中的 Bridge 源码和历史文档只为迁移参考保留，不应再部署到生产。

完成一次服务器和 GitHub 配置后，日常上线只需要推送 `main`：

```text
push main
  -> CI
  -> Auto Release 自动递增 patch、提交 VERSION、打 tag
  -> Release 构建单个 Sub2API 镜像
  -> Deploy Production 解析不可变 digest
  -> 通过 Tailscale + 受限 SSH 部署
  -> 服务器备份 PostgreSQL、部署、健康检查、公网冒烟测试
  -> 失败自动回滚 Sub2API 应用镜像
  -> Telegram 通知
```

不需要手工打 tag、点 Deploy、登录服务器或审批 Environment。

## 1. 生产架构

```text
Internet
  -> Cloudflare Proxy（橙色云）
  -> 源站 443（只允许 Cloudflare 来源）
  -> Caddy（Cloudflare Origin CA）
  -> api.xddapi.org.uk
       -> Sub2API
            -> PostgreSQL
            -> Redis
```

生产服务只有：

```text
caddy
sub2api
postgres
redis
```

支付由 Sub2API 内置 Provider 处理，支持：

- EasyPay / ZPay 兼容协议
- 支付宝官方
- 微信官方
- Stripe
- Airwallex

支付凭证在 Sub2API 管理后台配置，不写入服务器 `.env`、Git 或发布日志。

关键边界：

- 公网只开放 `443`，主机 UFW 与 Docker `DOCKER-USER` 双重限制为
  Cloudflare 来源。
- SSH `22` 只允许从 `tailscale0` 进入；GitHub Actions 使用 Tailscale
  OAuth 加入 `tag:ci`。
- 部署 SSH key 被强制绑定到 `/opt/sub2api/bin/deploy-release`，不能开
  shell、转发端口或执行任意命令。
- Sub2API、PostgreSQL、Redis 都不发布宿主机端口。
- 生产应用镜像固定为 `@sha256:...`，不使用可漂移的 `latest`。
- 自动回滚只回滚 Sub2API 应用镜像，不自动恢复数据库。

## 2. 域名、DNS 和证书

仅使用一个公开业务域名：

```text
https://api.xddapi.org.uk
```

支付页面：

```text
https://api.xddapi.org.uk/payment
```

Cloudflare DNS 应维护：

```text
api.xddapi.org.uk A     149.104.31.126          Proxied
api.xddapi.org.uk AAAA  2001:df1:7880:2::3a7    Proxied
```

发布脚本默认只维护 `api.xddapi.org.uk`，不会自动删除旧的
`pay.xddapi.org.uk`。旧支付域名应在原生支付稳定运行后再人工下线。

Cloudflare 配置：

1. SSL/TLS mode 使用 `Full (strict)`。
2. 开启 `Always Use HTTPS`。
3. 在 `SSL/TLS -> Origin Server` 创建 Origin CA 证书。
4. 证书 Hostname 只需覆盖 `api.xddapi.org.uk`。

自动发布会设置 Zone 级 `ssl=strict` 和 `always_use_https=on`。若同一
Zone 还有其他主机名，必须确保这些主机名也有有效 HTTPS 源站。

旧路由行为：

```text
/shop      -> 308 /payment
/shop/*    -> 308 /payment
/internal/* -> 404
```

`/shop` 只用于承接旧书签，不再代理 Bridge。`/internal/*` 是已退役的
Bridge 回调面，必须保持不可访问。

## 3. 一次性配置总览

上线前完成一次：

1. Cloudflare Origin CA 证书、API Token 和 API DNS 记录。
2. Tailscale ACL、服务器 auth key、GitHub Actions OAuth client。
3. GitHub Actions 专用 SSH key，并人工核对 SSH host key。
4. 服务器执行 `bootstrap-server.sh`。
5. GitHub 配置 Secrets/Variables，不增加人工审批。
6. 在现有 Sub2API 后台配置原生支付并完成一笔低金额真实支付验收。
7. 安装新的生产 Compose/Caddy/脚本并触发首次零按钮发布。
8. 停止旧 Bridge/Dujiao 容器，保留数据观察 7–14 天。

第 1–4 步需要服务器和 Cloudflare 管理权限；第 6 步需要支付商户后台
权限。完成之后，日常发布无需人工操作。

## 4. Cloudflare API Token

Cloudflare Dashboard：

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

Zone Resources 只授权 `xddapi.org.uk`。记录 Zone Overview 中的 Zone ID。

部署工作流会：

- 校验/创建 `api.xddapi.org.uk` 的 A 和 AAAA 记录。
- 将记录设为 Proxied。
- 保持每个地址族只保留一条由发布流程管理的记录。
- 设置 `ssl=strict` 和 `always_use_https=on`。

## 5. Tailscale

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

创建带 `tag:sub2api-prod` 的服务器 auth key。初始化时建议把主机名固定为
`sub2api-prod`。

`PROD_SSH_HOST` 使用 MagicDNS 短名或全名，不使用公网 IP。

### 5.2 GitHub Actions OAuth client

在 Tailscale Admin Console 创建 OAuth client：

- 允许创建 `tag:ci` 节点。
- 允许创建 auth key 所需 scope。
- 保存 `TS_OAUTH_CLIENT_ID` 和 `TS_OAUTH_SECRET`。

工作流会启动临时 tailnet 节点，任务结束后自动退出。

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

确认一致后，把 `ssh-keyscan` 输出原文保存为 `PROD_SSH_KNOWN_HOSTS`。
Actions 不会在运行时临时扫描并信任 host key。

## 7. 初始化服务器

首次通过服务器控制台或临时公网 SSH 登录，取得代码：

```bash
git clone https://github.com/OliverPanda/sub2api.git
cd sub2api
```

上传以下文件到服务器：

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

如果 GHCR package 仍是 Private，再增加：

```bash
GHCR_USERNAME=OliverPanda \
GHCR_TOKEN='github_pat_...' \
```

脚本会：

- 安装 Docker Engine、Compose plugin、UFW、Tailscale 等。
- 安装生产 Compose、Caddyfile 和运维脚本到 `/opt/sub2api`。
- 创建受限用户 `sub2api-deploy`。
- 生成 PostgreSQL、Redis、JWT、TOTP 和初始管理员密码。
- 首次生成后保留已有 `.env` 和已有凭据，不覆盖现网秘密。
- 只校验 `api.xddapi.org.uk` 的 Origin 证书 SAN。
- 安装受限 SSH 公钥。
- 配置 UFW、Cloudflare 来源限制和每周网段刷新 timer。
- 不启动、不删除、不修改任何旧 Bridge/Dujiao 容器或数据。

首次生成的凭据位于：

```text
/root/xddapi-initial-credentials.txt
```

保存到密码管理器后删除该文件。

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

主机 UFW 不能替代云厂商安全组。确认 Tailscale SSH 可用后，再关闭云厂商
安全组中的公网 SSH。

## 8. GitHub Secrets 和 Variables

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

可选 Secrets：

| Secret | 用途 |
| --- | --- |
| `PROD_SSH_PORT` | SSH 端口，默认 workflow fallback 为 `22` |
| `TELEGRAM_BOT_TOKEN` | 发布通知 |
| `TELEGRAM_CHAT_ID` | 发布通知 |

`RELEASE_TOKEN` 用于 Auto Release 向 `main` 提交 `VERSION` 并原子推送
tag，必须能够绕过当前分支保护写入 `main`。默认 `GITHUB_TOKEN` 不能完成
跨工作流写回。

### 8.2 Variables

| Variable | 默认值 |
| --- | --- |
| `ORIGIN_IPV4` | `149.104.31.126` |
| `ORIGIN_IPV6` | `2001:df1:7880:2::3a7` |
| `CLOUDFLARE_DNS_RECORD_NAMES` | `api.xddapi.org.uk` |
| `SIMPLE_RELEASE` | 必须为 `false` 或不设置 |

`SIMPLE_RELEASE=true` 只发布 amd64 镜像，不满足生产多架构发布要求。

不要给 `production` Environment 增加 Required reviewers，否则零按钮发布
会变成人工审批发布。

## 9. Sub2API 原生支付配置

登录：

```text
https://api.xddapi.org.uk
```

进入：

```text
管理后台
  -> 设置
  -> 支付设置
```

### 9.1 启用支付

至少配置：

- 启用支付。
- 最低/最高充值金额。
- 每日限额。
- 订单超时时间。
- 最大待支付订单数。
- 帮助文本或客服二维码。

### 9.2 添加 Provider 实例

进入 `服务商管理 -> 添加服务商`。回调地址由系统按
`SERVER_FRONTEND_URL` 自动拼接，必须确认域名是：

```text
https://api.xddapi.org.uk
```

#### EasyPay / ZPay

| 参数 | 内容 |
| --- | --- |
| Provider type | `EasyPay` |
| PID | ZPay/易支付商户 PID |
| PKey | ZPay/易支付商户密钥 |
| API base | 商户后台提供的 EasyPay API 地址 |
| 支付宝通道 ID | 可选 |
| 微信通道 ID | 可选 |

EasyPay 是第三方聚合支付协议。由 Sub2API 原生处理不等于官方支付宝/微信
直连；有官方商户资质时优先使用官方 Provider。

#### 支付宝官方

需要：

- AppID
- RSA2 应用私钥
- 支付宝公钥

桌面端优先扫码，移动端默认 WAP；需要当面付/扫码权限时才启用
“支付宝移动端当面付唤起”。

#### 微信官方

需要：

- AppID
- 商户号 MchID
- 商户 API 私钥
- 32 位 APIv3 密钥
- 微信支付公钥及公钥 ID
- 商户证书序列号

#### Stripe / Airwallex

按后台字段填写商户凭证。Stripe 还需要在 Stripe Dashboard 中配置
Webhook Secret。

### 9.3 明确前台支付来源

前台只展示“支付宝”和“微信支付”两个按钮。启用 Provider 后，还必须在
“前台可见支付方式路由”中分别指定：

- 支付宝按钮 -> 支付宝官方或 EasyPay 支付宝。
- 微信支付按钮 -> 微信官方或 EasyPay 微信。

同一按钮同一时刻只能路由到一个来源。未选择来源时，即使按钮启用，前台
也不会开放该支付方式。

### 9.4 Webhook 地址

```text
EasyPay:
https://api.xddapi.org.uk/api/v1/payment/webhook/easypay

支付宝官方:
https://api.xddapi.org.uk/api/v1/payment/webhook/alipay

微信官方:
https://api.xddapi.org.uk/api/v1/payment/webhook/wxpay

Stripe:
https://api.xddapi.org.uk/api/v1/payment/webhook/stripe
```

支付平台必须能通过公网 HTTPS 访问这些地址。Cloudflare 代理保持开启；
不要在源站额外放行非 Cloudflare 来源的 `443`。

### 9.5 支付页面

```text
https://api.xddapi.org.uk/payment
```

`/shop` 和 `/shop/*` 会 308 到这里。

## 10. 上线验收与切换顺序

不要先切换 Caddy 再验证支付。新版 Caddy 生效后 `/shop` 会立即跳到
`/payment`，如果原生 Provider 尚未验收，会产生支付入口空窗。

严格按以下顺序：

### 第一步：保留旧链路

保持当前 Bridge/Dujiao 容器运行。先完成 Sub2API PostgreSQL 备份：

```bash
cd /opt/sub2api
./bin/backup-postgres.sh pre-native-payment
```

### 第二步：配置原生 Provider

按第 9 节配置至少一个可用 Provider，并为支付宝/微信明确选择支付来源。

### 第三步：完成真实低金额验收

用最低允许金额发起一笔真实支付，确认：

- 创建订单成功。
- 支付平台收到正确金额、币种和订单号。
- Webhook 验签通过。
- 订单从 `PENDING -> PAID -> COMPLETED`。
- 用户余额只增加一次。
- 重复/伪造回调不会重复入账。
- 支付平台后台显示的回调地址是 `api.xddapi.org.uk`。
- 支付成功后返回 `/payment`，页面能显示成功状态。

至少分别验证准备开放的支付按钮。未完成真实验收前，不执行第 4 步。

### 第四步：安装新部署文件并发布应用

确保切换原生支付的代码已经合并。之后推送 `main` 触发完整零按钮链路，
或在首次迁移时手动运行一次 `Deploy Production` 使用现有 stable release。

新发布将：

- 部署 digest 固定的 Sub2API。
- 使用新 Compose 仅管理 `caddy/sub2api/postgres/redis`。
- 启用 `/shop -> /payment` 和 `/internal/* -> 404`。
- 运行 `/health`、`/setup/status`、重定向和旧回调阻断冒烟测试。

若 Sub2API 或 Caddy 不健康、冒烟测试失败，工作流会尝试恢复上一版
Sub2API 镜像。

### 第五步：灰度退役旧容器

新入口稳定、支付验收通过后，在服务器执行：

```bash
sudo /opt/sub2api/bin/retire-legacy-payment.sh
```

该脚本只会停止 Compose project `xdd-api` 中标签完全匹配的：

```text
bridge
bridge-postgres
dujiao-next
dujiao-postgres
dujiao-redis
```

它只执行 `docker stop`，不删除容器、卷、镜像、绑定目录或数据，可重复执行。

### 第六步：观察再清理

观察 7–14 天：

- 支付成功率和回调延迟正常。
- 没有漏单、重复入账或余额异常。
- `/shop` 旧书签访问量可接受。
- 旧支付域名没有剩余依赖。

观察期结束后，再人工删除 `pay.xddapi.org.uk` 的 DNS 记录。之后才考虑
清理旧容器、镜像和数据。任何时候都不要使用：

```bash
docker volume prune
docker compose down -v
```

## 11. 日常发布

正常开发：

```bash
git switch main
git pull --ff-only
# 修改代码并提交
git push origin main
```

随后自动发生：

1. `CI` 校验代码。
2. `Auto Release` 在 CI 成功后计算下一个 patch 版本。
3. 自动提交 `backend/cmd/server/VERSION`、创建 annotated tag，并原子推送
   `main + tag`。
4. `Release` 构建并发布单个 Sub2API 镜像：
   `ghcr.io/oliverpanda/sub2api:vX.Y.Z`。
5. `Deploy Production` 只接受 GitHub latest stable release，并解析
   Sub2API 不可变 digest。
6. 自动维护 `api.xddapi.org.uk` DNS 和 Zone TLS 设置。
7. Runner 加入 `tag:ci`，通过 Tailscale SSH 执行受限部署命令。
8. 服务器备份 PostgreSQL、拉取固定 digest、重建 Sub2API 和 Caddy。
9. 执行数据库、容器、Caddy 和公网冒烟检查。
10. 失败时自动恢复上一版 Sub2API 应用镜像。
11. Telegram 发送结果。

只有可能影响运行时产物的路径才会触发自动发版，例如：

```text
backend/
frontend/
Dockerfile.goreleaser
deploy/docker-entrypoint.sh
.goreleaser*.yaml
```

纯 README、FAQ、运营文档提交只跑 CI，不产生新版本。

首次部署或故障恢复时可手动运行一次 `Deploy Production`。之后继续恢复
push `main` 的零按钮模式。

发布约束：

- 版本严格为 `vMAJOR.MINOR.PATCH`。
- 服务器拒绝低于当前版本的 tag，防止迟到工作流降级。
- `concurrency: sub2api-production` 串行化生产部署。
- Tag 不移动、不复用。
- Release 只构建 Sub2API，不再构建 Bridge 镜像。

## 12. 部署协议、回滚和数据库

服务器只接受两种受限命令：

```text
deploy <version> <sub2api-digest> <git-revision>
rollback
```

当前 GitHub 工作流发送三参数 `deploy` 协议。脚本暂时兼容旧四参数协议，
但会忽略已退役的 Bridge digest。

需要人工回滚应用镜像时：

```bash
ssh -i ./sub2api-prod-deploy \
  sub2api-deploy@sub2api-prod \
  rollback
```

自动/人工应用回滚行为：

1. 读取 `state/release.env.previous`。
2. 拉取上一版固定 digest。
3. 先备份当前 PostgreSQL。
4. 重建上一版 Sub2API。
5. 等待健康检查并重启 Caddy。
6. 执行公网冒烟测试。
7. 写入 `releases.log`。

数据库迁移是 forward-only。应用回滚不会恢复 PostgreSQL，也不会恢复旧
Bridge/Dujiao 数据。数据库恢复必须作为单独的人工变更执行。

## 13. 日常运维

```bash
cd /opt/sub2api

# 当前版本与 digest
cat state/current-version
cat state/current-sub2api-digest

# 容器状态
docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'

# 应用日志
docker compose \
  --env-file .env --env-file state/release.env \
  -f compose.yml logs -f --tail=200 sub2api

# Caddy 日志
docker compose \
  --env-file .env --env-file state/release.env \
  -f compose.yml logs -f --tail=200 caddy

# 手动备份
./bin/backup-postgres.sh manual
```

健康检查：

```bash
curl -fsS https://api.xddapi.org.uk/health
curl -fsS https://api.xddapi.org.uk/setup/status
curl -sSI https://api.xddapi.org.uk/shop
curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST -H 'Content-Type: application/json' \
  -d '{"order_id":0,"status":"paid"}' \
  https://api.xddapi.org.uk/internal/order-paid
```

期望：

```text
/health                 -> status=ok
/setup/status           -> needs_setup=false
/shop                   -> 308, Location: /payment
/internal/order-paid    -> 404
```

## 14. 故障排查

### 发布被跳过

检查：

- 最新 Release 是否为 stable `vX.Y.Z`。
- Release 的 SHA 是否与 `main` 当前 SHA 一致。
- 改动是否只涉及文档。
- Auto Release 的 `RELEASE_TOKEN` 是否可写 `main` 和 tag。

### Cloudflare 521/522/523/525

依次检查：

```bash
curl -vk --resolve api.xddapi.org.uk:443:149.104.31.126 \
  https://api.xddapi.org.uk/health
docker ps
docker logs xdd-api-caddy-1 --tail=200
```

同时确认：

- `api` A/AAAA 记录指向正确服务器。
- Cloudflare 是 Proxied。
- Origin 证书 SAN 覆盖 `api.xddapi.org.uk`。
- 源站 443 对 Cloudflare CIDR 开放。
- Caddy 和 Sub2API 健康。

### 支付成功但余额未到账

检查：

- Provider 的实际回调 URL 是否为 `api.xddapi.org.uk`。
- Cloudflare 是否拦截了回调。
- Sub2API 日志中是否有验签失败或金额/订单号不匹配。
- Provider 控制台是否显示重试。
- 订单状态和站内支付订单明细。
- 不要手工重复加余额；先核对 Provider 订单号和本地幂等记录。

### `/shop` 没有跳到 `/payment`

```bash
curl -sSI https://api.xddapi.org.uk/shop
docker logs xdd-api-caddy-1 --tail=100
```

确认新 Caddyfile 已安装且 Caddy 已重建。

### 部署失败但站点仍可用

优先查看 GitHub Actions 日志和服务器：

```bash
tail -n 200 /opt/sub2api/logs/deploy.log
cat /opt/sub2api/state/release.env
cat /opt/sub2api/state/release.env.previous
```

若自动回滚成功，不要立即重复发布；先修复失败原因。若应用回滚成功但
数据库迁移不兼容，需要人工恢复数据库，不能只回滚镜像。

## 15. 上线检查清单

```text
[ ] Cloudflare 只保留 api.xddapi.org.uk 作为当前生产入口
[ ] A/AAAA 记录均为 Proxied
[ ] SSL/TLS 为 Full (strict)，Always Use HTTPS 开启
[ ] Origin 证书 SAN 覆盖 api.xddapi.org.uk 且与私钥匹配
[ ] Tailscale ACL 只允许 tag:ci -> tag:sub2api-prod tcp:22
[ ] 服务器 SSH 22 只允许 tailscale0
[ ] 源站 443 只允许 Cloudflare CIDR
[ ] GitHub Secrets/Variables 完整
[ ] 没有 production Environment 人工审批
[ ] Sub2API 原生支付 Provider 已启用
[ ] 支付宝/微信按钮已明确指定唯一来源
[ ] 每个开放渠道完成低金额真实验收
[ ] 订单 PENDING -> PAID -> COMPLETED
[ ] 余额只入账一次
[ ] /health 返回 ok
[ ] /setup/status 返回 needs_setup=false
[ ] /shop 308 到 /payment
[ ] /internal/order-paid 返回 404
[ ] 自动回滚所需上一版 release 元数据存在
[ ] 旧容器已灰度停止但数据尚未删除
[ ] 支付、回调、余额和对账观察 7–14 天正常
```

## 16. 不再需要 Dujiao 和 Bridge

生产不再需要：

- Dujiao-Next
- Dujiao PostgreSQL / Redis
- Bridge
- Bridge PostgreSQL
- Bridge Admin Token
- Dujiao Channel Key/Secret
- `pay.xddapi.org.uk`
- `/shop/healthz`
- `/internal/order-paid`

原生支付配置以 [`docs/PAYMENT_CN.md`](../../../docs/PAYMENT_CN.md) 为主。
以下文档只保留为历史设计资料，不应据此部署生产：

- `docs/DUJIAO_SUB2API_BRIDGE_CN.md`
- `docs/PAYMENT_HK_CN.md`
- `docs/PAYMENT_INTERACTION_CN.md`

Bridge 源码、历史 Compose 备份和旧数据可以先保留；在原生支付验收及观察期
结束前，不建议删除任何旧容器或数据。
