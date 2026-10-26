# Dujiao-Next 下单、Sub2API 余额到账 Bridge

> [!WARNING]
> **历史设计资料，已停止用于生产。**
> XDD API 生产已正式切换为 **Sub2API 原生支付**，生产 Compose、Caddy
> 和发布链路不再部署或依赖 Dujiao-Next / Bridge。请勿按本文创建
> `pay.xddapi.org.uk`、Bridge 回调或相关容器。现行方案见
> [`PAYMENT_CN.md`](PAYMENT_CN.md) 和
> [`deploy/ops/production/README.md`](../deploy/ops/production/README.md)。

## 1. 这套 Bridge 解决什么问题

Sub2API 原生充值适合已有支付通道和自建收银台的场景。本项目的 Bridge 把充值拆成两个更容易替换的部分：

```text
Sub2API 用户登录态
        |
        v
https://api.xddapi.org.uk/shop/
        |
        | 创建 Dujiao-Next 订单（档位不绑定渠道）
        v
Dujiao-Next（pay.xddapi.org.uk）
        |
        | 支付成功回调（HMAC 签名）
        v
Bridge 服务端重新查单
        |
        | 金额、币种、订单 ID、支付状态全部一致
        v
Sub2API Admin API 幂等充值
        |
        v
用户余额到账
```

Bridge 不保存银行卡、支付平台密钥，也不直接相信浏览器返回的“已支付”。支付成功必须由 Dujiao 回调触发，并且 Bridge 会再次向 Dujiao 查询订单；只有服务端确认后才调用 Sub2API 入账。

## 2. 生产域名和服务

| 地址 | 服务 | 用途 |
| --- | --- | --- |
| `https://api.xddapi.org.uk` | Sub2API | 登录、控制台、API |
| `https://api.xddapi.org.uk/shop/` | Bridge | 同源充值页、订单查询、管理接口 |
| `https://api.xddapi.org.uk/internal/order-paid` | Bridge | Dujiao 支付成功回调 |
| `https://api.xddapi.org.uk/internal/order-fulfilled` | Bridge | Dujiao 履约完成回调 |
| `https://pay.xddapi.org.uk` | Dujiao-Next | 商城、商品、支付通道、Dujiao 管理后台 |

同源挂载 `/shop/` 的目的，是让 Bridge 从 Sub2API 的 `localStorage.auth_token` 读取登录态；不要把 JWT 拼进 URL，URL 会进入浏览器历史、代理日志和 Referer。

生产 Compose 中相关服务：

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

Caddy 将 `api.xddapi.org.uk` 的 `/shop/*` 和 `/internal/order-*` 路由到 Bridge，其余请求路由到 Sub2API；`pay.xddapi.org.uk` 路由到 Dujiao-Next。

## 3. 状态和资金安全

Bridge 订单状态：

```text
created
  -> pending_payment
  -> paid
  -> crediting
  -> credited

pending_payment / paid / crediting
  -> credit_failed -> 自动延迟重试

created / pending_payment
  -> canceled
paid 后退款
  -> refunded
```

关键不变量：

1. **服务端价格校验**：SKU 的 `price`、`credit`、`product_id`、`sku_id` 只在服务端配置中读取，浏览器只能提交 SKU key 和数量。
2. **创建订单后二次校验**：Dujiao 实际订单金额和币种必须与本地 SKU 计算结果完全一致，否则取消 Dujiao 订单并返回 `PRICE_CHANGED`。
3. **档位与支付渠道解耦**：创建 Bridge/Dujiao 订单时不提交 `channel_id`。订单创建完成后，再按该订单查询所有允许的支付渠道；只有一个渠道时自动进入支付，多个渠道时才展示具体付款方式。
4. **回调验签**：校验 `Dujiao-Next-Channel-Key`、时间戳、HMAC-SHA256 签名和回调路径。
5. **回调不是到账依据**：验签后仍用 Dujiao Channel API 重新查询订单，核对 Dujiao 订单 ID、币种、订单金额和实付金额。
6. **幂等入账**：Bridge 订单号生成固定充值码 `xddshop-<bridge-order-no>`，同时传入 `Idempotency-Key: redeem-<bridge-order-no>`。重复回调不会重复加余额。
7. **先记支付，后入账**：Sub2API Admin Key 未配置或暂时调用失败时，Dujiao 已支付订单会保留为 `credit_failed`，不会因为 Bridge 重启而丢失。
8. **不信任客户端金额**：客户端不传余额数量，Bridge 按 `sku.credit × quantity` 计算到账金额。

> 不要同时启用 Sub2API 原生充值和本 Bridge，否则同一用户可能看到两套充值入口、两套订单和两套对账口径。确定使用 Bridge 后，以 `/shop/` 为唯一充值入口。

## 4. SKU 配置

文件：

```text
/opt/sub2api/bridge/config/skus.json
```

仓库模板：

```text
deploy/ops/production/bridge/config/skus.json
```

模板不能直接上线，必须填入真实 Dujiao 商品 ID、SKU ID、价格和到账金额。示例：

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
  },
  "cny-50": {
    "display_name": "50 元充值",
    "product_id": 12,
    "sku_id": 35,
    "price": "50.00",
    "credit": "50.00",
    "currency": "CNY",
    "credit_unit": "余额",
    "min_quantity": 1,
    "max_quantity": 5,
    "recommended": true,
    "description": "适合常规使用"
  }
}
```

规则：

- `product_id`、`sku_id` 必须是 Dujiao 后台真实存在且启用的 ID。
- `price` 和 `credit` 是两位小数的金额字符串；`price` 是用户支付金额，`credit` 是 Sub2API 余额增加量。
- `currency` 必须与 Dujiao 商品 SKU 的币种一致，例如 `CNY`。
- `min_quantity` 至少为 1，`max_quantity` 不得小于 `min_quantity`。
- 修改 SKU 后需要重启 Bridge 容器让配置重新加载。
- 生产环境不要使用“前端显示 10 元、Dujiao 实际 1 元”的价格漂移配置；Bridge 会拒绝创建订单。

## 5. Dujiao Channel Client 配置

在 Dujiao-Next 管理后台创建 Channel Client。字段名称可能随版本略有不同，但必须满足：

```text
channel_type: telegram_bot
callback_url: https://api.xddapi.org.uk
```

Dujiao 会根据该 base URL 生成并回调：

```text
https://api.xddapi.org.uk/internal/order-paid
https://api.xddapi.org.uk/internal/order-fulfilled
```

如果 Channel Client 选择其他类型，Dujiao 可能不发送 Bridge 所需回调。创建后把 Channel Key 和 Secret 写入服务器 `.env`：

```dotenv
DUJIAO_CHANNEL_KEY=...
DUJIAO_CHANNEL_SECRET=...
```

再把 Channel Client 对应的支付通道在 Dujiao 后台启用。Bridge 通过以下接口读取支付方式：

```text
GET /api/v1/channel/payment-channels
```

`payment_configured` 为 `true` 只表示 Bridge 已拿到 Channel Key/Secret，不表示支付通道已审核通过；支付方式是否可用以 Dujiao 后台和 `/api/payment-channels` 返回结果为准。

如果商品只在部分支付通道上架，可在查询时传入 Bridge 订单号：

```text
GET /api/payment-channels?order_no=xdd-20261023T120000-abcd1234
```

Bridge 会先校验该订单属于当前登录用户，再让 Dujiao 按订单商品过滤支付通道，避免展示商品不允许的支付方式。

档位目录本身不保存支付渠道。用户确认档位和数量后，Bridge 才创建订单并调用上述接口获取该订单可用渠道：

- 单渠道：自动创建支付单，用户无需再次选择。
- 多渠道：展示 Dujiao 返回的全部可用付款方式，用户只选择付款方式，不发生档位与渠道绑定。

Dujiao Channel API 当前没有“不指定渠道的统一收银台支付单”接口，因此多渠道场景不能诚实地做成零选择。若后续接入支持聚合收银台的单一 Provider，可再移除这一步付款方式选择。

## 6. Sub2API Admin API Key

Bridge 入账调用：

```http
POST /api/v1/admin/redeem-codes/create-and-redeem
x-api-key: <SUB2API_ADMIN_API_KEY>
Idempotency-Key: redeem-<bridge-order-no>
```

在 Sub2API 管理后台生成专用 Admin API Key，只交给 Bridge 使用，然后写入：

```dotenv
SUB2API_ADMIN_API_KEY=...
```

不要把该 Key 放进前端、Git、聊天记录或 `skus.json`。Key 轮换后更新 `.env` 并重启 Bridge。`GET /shop/healthz` 的 `crediting_configured` 应为 `true`。

## 7. 其他生产环境变量

```dotenv
BRIDGE_PUBLIC_URL=https://api.xddapi.org.uk
BRIDGE_ADMIN_TOKEN=...
BRIDGE_SUPPORT_URL=https://...
BRIDGE_SUPPORT_LABEL=联系客服
BRIDGE_POLL_INTERVAL=20s
BRIDGE_REQUEST_TIMEOUT=20s
BRIDGE_CALLBACK_MAX_SKEW=60s
BRIDGE_CREDIT_RETRY_BACKOFF=2m
DUJIAO_BASE_URL=http://dujiao-next:8080
DUJIAO_CHANNEL_KEY=...
DUJIAO_CHANNEL_SECRET=...
SUB2API_ADMIN_API_KEY=...
```

`BRIDGE_ADMIN_TOKEN` 只用于管理员订单查询和人工重试：

```bash
curl -fsS \
  -H "Authorization: Bearer ${BRIDGE_ADMIN_TOKEN}" \
  "https://api.xddapi.org.uk/shop/admin/orders?status=credit_failed"
```

## 8. 部署和首次上线顺序

1. 准备香港服务器、Cloudflare Zone 和 Origin 证书。
2. Origin 证书 SAN 必须同时覆盖 `api.xddapi.org.uk` 与 `pay.xddapi.org.uk`。
3. 执行 `deploy/ops/production/bootstrap-server.sh`，生成服务器密钥和初始管理员凭据。
4. 确认 Sub2API、Bridge、Dujiao、两个数据库和两个 Redis 均健康。
5. 登录 Dujiao 管理后台，创建 `telegram_bot` Channel Client。
6. 在 Dujiao 后台创建支付通道、商品和 SKU。
7. 编辑服务器上的 `skus.json`，填入真实商品 ID、SKU ID、价格和到账金额。
8. 把 Channel Key/Secret、Sub2API Admin API Key、客服链接写入 `.env`。
9. 重启 `bridge`，检查 `https://api.xddapi.org.uk/shop/healthz`。
10. 用一个低金额测试订单完整验证：下单、支付、回调、查单、余额到账、订单记录。
11. 验证重复回调和 Bridge 重启后不会重复加余额。
12. 通过 `push main` 走后续应用版本的零按钮发布。

## 9. 上线检查清单

```text
[ ] api.xddapi.org.uk 可登录 Sub2API
[ ] pay.xddapi.org.uk 可打开 Dujiao
[ ] /shop/ 可读取当前登录用户
[ ] /shop/healthz 返回 status=ok
[ ] payment_configured=true
[ ] crediting_configured=true
[ ] skus.json 不为空，商品和 SKU ID 正确
[ ] Dujiao 支付方式能显示在 /shop/
[ ] 测试订单支付成功后余额只增加一次
[ ] credit_failed 订单可通过管理接口重试
[ ] 客服入口可打开
[ ] 生产 `.env` 和 Key 未进入 Git
```

## 10. 排障

| 现象 | 优先检查 |
| --- | --- |
| 打开 `/shop/` 提示未登录 | 是否从同一主域登录 Sub2API；浏览器是否清理了 `auth_token` |
| `payment_configured=false` | `DUJIAO_CHANNEL_KEY`、`DUJIAO_CHANNEL_SECRET` 是否同时设置；Bridge 是否重启 |
| `crediting_configured=false` | `SUB2API_ADMIN_API_KEY` 是否设置；Sub2API Admin API 是否可用 |
| 页面显示“暂未配置充值套餐” | `skus.json` 为空或 JSON 无效 |
| 返回 `PRICE_CHANGED` | Dujiao 商品价格/币种与 `skus.json` 不一致 |
| 返回 `PAYMENT_CHANNELS_UNAVAILABLE` | Dujiao Channel API、Channel Client 或支付通道异常 |
| 支付成功但余额不到账 | 查看 `credit_failed` 订单和 `last_error`；确认 Admin Key；修好后调用重试 |
| 回调返回 401 | Channel Secret、签名算法、时间偏差（默认 60 秒）或代理是否改写路径 |
| 回调返回 503 | Bridge 尚未配置 Dujiao Channel Key/Secret |
| 用户已付款但订单被取消 | 先查 Dujiao 支付流水，人工核对后处理；不要直接手工加余额 |
| Bridge 重启后订单丢失 | 检查 `bridge_postgres_data` 是否被误删；不要把 Bridge 数据库当作可丢弃缓存 |

查看日志：

```bash
cd /opt/sub2api
docker compose --env-file .env --env-file state/release.env -f compose.yml logs --tail=200 bridge
docker compose --env-file .env --env-file state/release.env -f compose.yml logs --tail=200 dujiao-next
```

## 11. 运维原则

- 所有“已支付”判断以 Dujiao 服务端订单为准。
- 所有余额变更以 `xddshop-<bridge-order-no>` 作为唯一业务幂等标识。
- 先保留支付订单和错误，再修复入账能力；不要删除 `credit_failed` 记录。
- 对账至少覆盖 Dujiao 订单、Bridge 订单、Sub2API 兑换码/余额流水三张表。
- 退款、拒付、人工补单必须走人工审核流程，并保留操作记录。
- 应用镜像可以自动回滚；数据库和已支付订单不能依赖镜像回滚恢复。
