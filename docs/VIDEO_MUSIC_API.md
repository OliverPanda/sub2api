# 视频与音乐接口及上游接入

本文描述当前工作区代码提供的媒体接口。Sub2API 负责 API Key 鉴权、分组权限、上游账号调度、任务归属和计费；上游供应商只负责生成任务和返回媒体结果。

## 1. 上线前配置

以下项目缺一不可：

| 配置 | 视频 | 音乐 |
| --- | --- | --- |
| PostgreSQL、Redis | 必须 | 必须 |
| Sub2API API Key、分组 | 必须 | 必须 |
| 分组模型白名单 | `video-seedance`、`video-minimax-h3` 等 | Replicate `owner/model` |
| 分组价格 | 按模型和清晰度配置每秒价格 | 按模型配置单次价格 |
| 上游账号 | OpenAI/LiteLLM 兼容视频账号 | OpenAI API Key 类型账号，能力 `music_generation` |
| 上游密钥 | LiteLLM、Ark 或 MiniMax 中转站密钥 | Replicate 密钥或兼容中转站密钥 |

工作区当前没有可用的 Ark、MiniMax、LiteLLM 或 Replicate 生产密钥；因此自动化测试可以验证协议、路由、归属和账务，但不能证明真实供应商已经开通或能实际出片。

## 2. 视频统一接口

### 创建

```http
POST /v1/videos
POST /v1/videos/generations
Authorization: Bearer sk-sub2api
Content-Type: application/json
```

请求：

```json
{
  "model": "video-minimax-h3",
  "prompt": "海浪拍打礁石，电影感镜头",
  "seconds": 5,
  "size": "1280x720",
  "resolution": "720p",
  "input_reference": "https://example.com/reference.png"
}
```

`model`、`prompt`、`seconds` 和 `size` 是兼容视频适配器的通用字段。Seedance 和 MiniMax H3/H3-Max 必须显式传 `resolution`，因为它同时决定上游质量档和计费档。支持的档位以账号模型映射和分组价格为准；没有匹配价格时创建会被拒绝。创建成功返回 Sub2API 任务 ID，例如 `mvid_...`，不会把用户切换到其他上游账号。

### 查询和下载

```http
GET /v1/videos/{id}
GET /v1/videos/{id}/content
```

查询必须使用创建任务的同一 API Key 和同一分组。状态通常为 `queued`、`in_progress`、`completed`、`failed` 或 `cancelled`。提交遇到网络超时、上游 5xx、缺少上游任务 ID 或本地绑定失败时，创建返回 502，但会返回本地任务 ID 并将状态记为 `submission_unknown`；余额冻结不会自动释放，后台会在冷却后用同一幂等键重试。任务未完成时下载返回冲突，不会把上游临时 URL直接暴露给客户端；完成后 `/content` 返回重定向或媒体响应。

通用 `/v1/videos` 路由当前没有统一的 DELETE 取消接口；供应商原生取消能力仍按原生适配器提供。

创建时按请求时长冻结余额；成功后按请求时长结算，失败或取消释放冻结。重复查询、并发查询和后台轮询通过任务表幂等结算，只扣一次。

## 3. Seedance 接入 LiteLLM

统一 `/v1/videos` 会把 Seedance 和 MiniMax 一样送入 `video_generation` 账号调度。Sub2API 将公开模型名映射为 LiteLLM 的 `volcengine/...` 模型名，并使用 LiteLLM 的 OpenAI 风格视频接口完成创建、查询和下载。Ark 原生协议仍保留在 `/api/v3/contents/generations/tasks`，用于需要原生字段的场景。

管理员操作：

1. 在 `deploy/litellm/config.yaml` 中启用 `volcengine/doubao-seedance-2-0-260128`，并配置 `ARK_API_KEY`、`ARK_API_BASE`；中转站需要兼容 LiteLLM 的 Volcengine 视频适配器。
2. 创建 OpenAI API Key 类型上游账号，`base_url` 填内部 LiteLLM HTTPS 地址，`api_key` 填 LiteLLM 内部 Key。
3. 账号能力勾选 `video_generation`，模型映射配置为 `video-seedance -> volcengine/doubao-seedance-2-0-260128`。
4. 把账号绑定到 OpenAI/Composite 分组，加入模型白名单，并配置 `480p/720p/1080p` 对应每秒价格。
5. 使用上面的 `/v1/videos` 请求做一次创建、状态和下载冒烟测试。

`prompt`、`seconds`、`size`、`resolution` 和 `input_reference` 会按兼容视频协议发送给 LiteLLM；Seedance 的 `resolution` 仅允许 `480p/720p/1080p`，实际可用范围由供应商权限决定。

## 4. MiniMax H3/H3-Max 接入

MiniMax 走 OpenAI/LiteLLM 兼容视频适配器，统一入口仍是 `/v1/videos`。

### LiteLLM 配置

在 `deploy/litellm/config.yaml` 中启用供应商模型，并在 `.env` 设置 `MINIMAX_API_KEY`、`MINIMAX_API_BASE`。建议先用固定版本镜像完成契约测试，再部署不可变镜像：

```sh
cd deploy/litellm
cp .env.example .env
docker build -t sub2api-litellm:1.110.0-media .
docker run --rm --network none --entrypoint python \
  sub2api-litellm:1.110.0-media /opt/media/verify_contract.py
docker compose --env-file .env -f compose.yml up -d
```

LiteLLM 只放在内网，通过 HTTPS 域名提供给 Sub2API。Sub2API 中创建 OpenAI API Key 类型账号：

- `base_url`: 内部 LiteLLM HTTPS 地址；
- `api_key`: LiteLLM 内部 key；
- 能力：`video_generation`；
- 模型映射：
  - `video-minimax-h3 -> minimax/MiniMax-H3`
  - `video-minimax-h3-max -> minimax/MiniMax-H3-Max`

为公开模型配置分组视频价格。当前接入档位：H3 支持 `768p`、`2K`；H3-Max 支持 `480p`、`768p`。客户端仍传 `resolution: "768p"` 或 `resolution: "2K"`，网关会转换为供应商所需的 `768P`/`2K`。

```sh
curl https://api.example.com/v1/videos \
  -H 'Authorization: Bearer sk-sub2api' \
  -H 'Content-Type: application/json' \
  -d '{"model":"video-minimax-h3","prompt":"海浪拍打礁石","seconds":5,"size":"1280x720","resolution":"768p"}'
```

## 5. 音乐统一接口（Replicate）

音乐不走 LiteLLM。当前统一入口使用 Replicate 的异步 prediction 协议，模型名必须是 `owner/model`。

### 创建

```http
POST /v1/music
Authorization: Bearer sk-sub2api
Content-Type: application/json
```

```json
{
  "model": "owner/music-model",
  "input": {
    "prompt": "upbeat electronic background music",
    "duration": 30
  }
}
```

成功返回 `music.task`，任务 ID 形如 `rmus_...`，状态为 `starting` 或 `processing`：

```json
{
  "id": "rmus_...",
  "object": "music.task",
  "model": "owner/music-model",
  "status": "processing"
}
```

### 查询、下载和取消

```http
GET    /v1/music/{music_id}
GET    /v1/music/{music_id}/content
DELETE /v1/music/{music_id}
```

`GET` 会轮询创建任务时绑定的 Replicate 账号；跨用户、跨 API Key 或跨分组访问会返回 404。提交不确定的任务会返回 `submission_unknown`，直到后台用同一幂等键完成绑定或由运维确认。任务成功后，`content` 从输出中解析字符串 URL、URL 数组或 `{url}`/`{audio_url}` 对象，并只接受 HTTPS 地址。取消只有在 Replicate 明确返回 `canceled` 后才释放冻结余额。

音乐价格按分组中匹配模型的单次价格计算。成功结算、失败/取消退款和重复轮询幂等都在任务表与统一 usage billing 事务中完成。上游返回不支持的输入字段时，错误原样作为上游失败返回。

### Replicate 账号配置

1. 在 Replicate 创建 API Token，或准备一个兼容 Replicate API 的中转站。
2. 在 Sub2API 创建 OpenAI API Key 类型上游账号：`base_url=https://api.replicate.com`（中转站填其 HTTPS 根地址）、`api_key` 填 Token。
3. 为账号增加能力 `music_generation`，绑定到目标分组。
4. 在分组模型价格中添加实际的 `owner/model` 和单次价格。
5. 用一个低成本模型先测试创建、轮询、下载和取消，再开放给业务用户。

## 6. 测试与当前结论

代码级测试：

```sh
cd backend
go test ./internal/service ./internal/handler ./internal/repository ./internal/server/routes
go test -tags unit ./internal/service -run 'TestSeedance|TestCompatible|TestVideo|TestReplicate' -count=1
go test -tags integration ./internal/repository \
  -run 'TestReplicateMusicSettlementIsAtomicAndIdempotent|TestCompatibleVideo|TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate' \
  -count=1
git diff --check
```

这些测试覆盖路由注册、Seedance 请求转换、LiteLLM 兼容视频创建/查询/下载、Replicate 音乐输出解析、任务归属和账务幂等。`deploy/litellm/verify_contract.py` 还可以在无外网、无真实密钥的容器中验证 Seedance/MiniMax 的 LiteLLM 协议转换。

真实接口冒烟还缺：

- 当前运行容器需要重新构建并部署本工作区代码，否则 `/v1/music` 仍可能返回 404；
- Ark `api_key`、MiniMax `api_key` 或 LiteLLM 内部供应商配置；
- Replicate `api_key` 或兼容中转站配置；
- Sub2API 中可用 API Key、账号能力、模型白名单和价格；
- 各模型供应商账户的实际生成权限和余额。

完成这些配置后，按“创建 -> 查询直到完成 -> content 下载 -> 失败/取消”各执行一次，才能确认真实上游闭环。
