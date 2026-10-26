# 并发与容量配置档位

本文用于单机生产部署（`/opt/sub2api`）的并发扩容。它不会自动修改线上 `.env`；修改后需要按第 5 节重建相关容器并观察。当前仓库里的模板不声称等于线上实际值，操作前必须在服务器上核对。

## 1. 先分清六层“并发”

“把并发调大”不是只改一个数字。请求会依次受到以下限制：

| 层级 | 主要配置 | 作用 |
| --- | --- | --- |
| 入站 HTTP/2 | `SERVER_H2C_MAX_CONCURRENT_STREAMS` | 单个 h2c 连接可并行多少流 |
| 用户/API Key 限流 | `default.user_concurrency`、用户和 Key 的并发/RPM 设置 | 防止单个租户占满容量 |
| 账号调度 | 账号 `concurrency`、调度等待队列 | 每个上游账号同时允许多少在飞请求 |
| OpenAI WebSocket 上游池 | `gateway.openai_ws.max_conns_per_account` 和动态系数 | 每个账号可同时持有多少上游会话/连接 |
| 普通 HTTP 上游池 | `GATEWAY_MAX_CONNS_PER_HOST`、`GATEWAY_MAX_IDLE_CONNS*` | 对同一上游主机的连接复用上限 |
| 数据层 | `DATABASE_MAX_OPEN_CONNS`、`REDIS_POOL_SIZE`、`POSTGRES_MAX_CONNECTIONS` | 请求处理过程中数据库和 Redis 的可用容量 |

因此，H2C stream、数据库连接数和 Redis 连接数**都不等于业务 QPS**。最终上限由 CPU、内存、上游账号池、调度队列和数据层共同决定。

## 2. 三档参考值

以下值针对**单实例**；多实例时，数据库连接和上游连接要乘以实例数。

| 配置 | 保守档（当前模板） | 推荐档（8C/16G 左右） | 高负载档（16C/32G 左右） |
| --- | ---: | ---: | ---: |
| `SERVER_H2C_MAX_CONCURRENT_STREAMS` | 50 | 128 | 256 |
| `POSTGRES_MAX_CONNECTIONS` | 300 | 512 | 1024 |
| `DATABASE_MAX_OPEN_CONNS` | 200 | 384 | 640 |
| `DATABASE_MAX_IDLE_CONNS` | 80 | 192 | 320 |
| `REDIS_POOL_SIZE` | 1024 | 4096 | 8192 |
| `REDIS_MIN_IDLE_CONNS` | 32 | 256 | 512 |
| `GATEWAY_MAX_CONNS_PER_HOST` | 2048 | 4096 | 8192 |
| `GATEWAY_MAX_IDLE_CONNS` | 8192 | 16384 | 32768 |
| `GATEWAY_MAX_IDLE_CONNS_PER_HOST` | 4096 | 8192 | 16384 |

说明：

- 推荐档和高负载档不是“保证容量”，只是没有超过常见连接池配比的安全起点。服务器规格未知时，先按推荐档并做压测。
- `POSTGRES_MAX_CONNECTIONS >= 实例数 × DATABASE_MAX_OPEN_CONNS × 1.2`。例如推荐档单实例要求至少 `384 × 1.2 ≈ 461`，所以使用 512。
- `DATABASE_MAX_IDLE_CONNS <= DATABASE_MAX_OPEN_CONNS`，推荐维持约 50%。
- `REDIS_MIN_IDLE_CONNS <= REDIS_POOL_SIZE`，否则启动时可能直接报配置错误。
- 上游连接池可以高于入站 stream，因为长连接、账号隔离和会话复用会产生额外连接；但不要超过宿主机文件描述符和上游承载能力。
- 生产 compose 已将 `nofile` 提到 100000，仍需用 `ulimit -n` 和 `ss -s` 核对实际状态。

## 3. 账号和 WebSocket 并发

业务并发通常先卡在账号调度层，而不是 H2C 或数据库。重点检查：

```yaml
gateway:
  openai_ws:
    max_conns_per_account: 128
    min_idle_per_account: 4
    max_idle_per_account: 12
    dynamic_max_conns_by_account_concurrency_enabled: true
    oauth_max_conns_factor: 5.0
    apikey_max_conns_factor: 5.0
    queue_limit_per_conn: 64
    pool_target_utilization: 0.7
```

- 账号实际可调度并发由管理后台中账号的 `concurrency` 决定。
- 动态连接上限约为 `min(max_conns_per_account, ceil(account.concurrency × factor))`。
- 如果账号并发为 20、OAuth 系数为 5，则动态连接上限约为 100，仍小于 128。
- 如果账号并发为 40，则动态上限会被 `max_conns_per_account: 128` 截断；此时需要同时评估调高 `max_conns_per_account`。
- WebSocket 会话可能在多轮请求之间继续持有连接，所以“在飞请求数”与“存活 WS 连接数”不是同一个指标。
- 用户级默认并发 `default.user_concurrency: 5` 只影响新用户默认值；已有用户应通过后台或数据库确认实际值。

扩容顺序建议：

1. 先增加健康上游账号或账号并发，避免只放大排队。
2. 再检查 `waiting_in_queue`、账号切换率和 429 比例。
3. 最后才提高上游连接池、Redis 池和数据库池。

不要为了消除 429 而无上限提高账号并发；这通常会把上游限流变成更严重的账号封禁风险。

## 4. 上线前检查

在服务器上执行以下只读检查，不要输出或提交 `.env` 内容：

```bash
cd /opt/sub2api
nproc
free -h
ulimit -n
docker stats --no-stream
docker compose ps
```

数据库侧检查：

```bash
docker compose exec -T postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "SHOW max_connections;"
```

Redis 侧检查：

```bash
docker compose exec -T redis \
  sh -ec 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli INFO clients'
```

同时观察 Ops 仪表盘中的 QPS、P95/P99、并发等待队列、数据库等待连接、Redis 活跃连接、goroutine、CPU 和内存。若队列或 P95 持续上升，继续加连接池只会加重问题。

## 5. 应用和回滚

修改前先备份配置：

```bash
cd /opt/sub2api
cp -p .env ".env.bak.$(date -u +%Y%m%dT%H%M%SZ)"
chmod 600 .env.bak.*
```

仅修改应用侧连接池时：

```bash
cd /opt/sub2api
docker compose up -d --no-deps --force-recreate sub2api
```

修改 `POSTGRES_MAX_CONNECTIONS` 需要重建 PostgreSQL 容器，属于有短暂停机风险的操作，应在维护窗口执行；不要把它当作普通热更新。修改 Redis 池通常只需重建应用，但如果同时调整 Redis 服务端 `maxclients`，也应安排维护窗口。

回滚：

```bash
cd /opt/sub2api
cp -p .env.bak.YYYYMMDDTHHMMSSZ .env
docker compose up -d --no-deps --force-recreate sub2api
```

若 PostgreSQL 服务端参数也改过，同样需要恢复 `.env` 后重建 `postgres`，并先确认应用已经停止写入或已完成备份。

## 6. 推荐的逐步扩容路径

1. 保持当前档位，记录 24 小时 QPS、P95、P99、429 比例和并发队列基线。
2. 把账号 `concurrency` 提高 25%，观察 30–60 分钟。
3. 若队列仍高但 CPU/内存和上游错误率正常，再提高 H2C stream 和上游池。
4. 若数据库等待连接明显上升，按公式提高 PostgreSQL 与应用 DB 池，而不是只提高应用上限。
5. 若 Redis 出现连接等待，先提高 Redis 池并检查慢命令和网络 RTT。
6. 每次只改一层，保留可回滚的时间窗口；不要一次把所有参数翻倍。

真正的自动发布仍然由 stable tag 触发；本文的容量参数属于环境配置，不会因为推送 `main` 自动生效。
