# API 参考

[文档导航](README.md) · [客户端接入](clients.md) · [模型路由](configuration.md)

| 接口 | 协议 | 流式 | 非流式 |
|---|:-:|:-:|:-:|
| `POST /v1/chat/completions` | OpenAI Chat Completions | ✅ | ✅ |
| `POST /v1/responses` | OpenAI Responses | ✅ | ✅ |
| `POST /v1/messages` | Anthropic Messages | ✅ | ✅ |
| `GET /v1/models` | 模型列表（含 `models.aliases` 声明的别名） | — | ✅ |
| `GET /metrics` | Prometheus 文本格式指标（与控制台指标同源；启用鉴权时需 API Key） | — | ✅ |
| `GET /health` | 健康检查（只报状态与平台 ID，不探测上游） | — | ✅ |

三个生成端点共用模型路由，支持文本、多轮对话，以及模型声明支持的思考、工具与图片能力。参数兼容性由实际平台决定：Cursor 暂不支持图片、采样、effort、JSON 输出模式及 stop，客户端 system 作为上下文转交，保留原生 Agent 规则。跨协议接入前请核对[平台能力对照](platforms.md#协议与能力)。

管理与调试界面在 `/`（见[网页控制台](console.md)），不属于 API。

## 鉴权

配置 `auth.api_key` 后，生成接口、模型列表、管理接口和 `/metrics` 需要网关访问密钥。Chat / Responses 通常使用 `Authorization: Bearer <网关密钥>`，Messages 通常使用 `x-api-key: <网关密钥>`；网关支持这两种形式。`/health` 和静态控制台不要求密钥。

## 模型目录

`GET /v1/models` 返回唯一裸名及各平台限定名。`GET /api/models` 提供完整元数据：`platform`、`routing_id`、`default_route`、上下文窗口、输出上限与能力。客户端配置推荐使用 `routing_id`。模式入口的名字不代表某个固定底层模型；没有元数据时不要猜测窗口大小。

新版本的 WorkBuddy 目录还包含 `pricing`：`status`（`free` 表示零积分倍率、`credits`、`dynamic`、`unknown`）、可选的 `credit_multiplier`、`upstream_label`、`source` 和 `observed_at`。来源为上游模型配置，按平台区分，不代表账号余额或金额单价；缺失倍率不能当作零。其他适配器或内置兜底目录可省略此字段。详见[控制台费率说明](console.md#模型费率)。

TRAE 保留 `pricing.status: "credits"` 与正积分倍率，并独立返回 `access`：`basis: "recurring_free_allowance"`、`plan: "free"`、`overage_enabled: false`、`remaining_credits`、`source: "trae_free_plan"`、`checked_at`。它表示近期核验的账号免费额度，不能推广到同名其他平台，也不能由历史费率快照补造。生成前仍重新检查。TRAE 的输出上限仅为目录值，max_tokens、采样与思考控制不生效，详见 [TRAE 限制](trae.md#参数和用量限制)。

`/api/models` 还会为已知护栏返回 `image_limit`（扁平目录和分平台目录均提供）：`max_images`、`enabled`、`source`（`builtin_empirical` / `config`），内置实测还包含 `observed_on`。未配置时省略该字段，不代表上游无限制；`max_images: 0, enabled: false` 表示本地显式关闭护栏。它描述的是网关当前有效规则，详见[图片护栏配置](configuration.md#单请求图片数量护栏modelsimage_limits)。

调用记录中的 `image_count` 是原请求图片内容块总数，含历史与工具结果；`max_images` 是当次实际平台的护栏值。旧记录没有这些字段，不应解释为 0 张。默认 `reject` 模式下，超限请求在上游调用前返回 `image_count_exceeded` / HTTP 400，并且不裁剪内容。

显式启用 `models.image_overflow_policies` 的 `trim_oldest` 时，已知超限请求改为省略最旧图片并继续转发；默认 `reject` 不变。`image_limit.overflow_policy` 提供有效策略；记录另含 `forwarded_image_count`（准备发往最后实际平台的数量）、`removed_image_count`、`image_overflow_policy`。清理响应头为 `X-Agent2API-Images-Removed` / `X-Agent2API-Images-Forwarded` / `X-Agent2API-Image-Overflow-Policy`，本地对话未被修改。护栏配置、行为和代价见[旧对话续聊策略](configuration.md#旧对话超限后的续聊策略modelsimage_overflow_policies)。

## 提示缓存与用量

生成接口返回 `X-Request-ID`；建流完成或失败时另带 `X-Upstream-Attempts` 和 `X-Queue-Ms`。请求详情中的 `request_id`、`attempt_count`、`queue_ms`、`attempts` 与响应对应，历史记录缺失字段表示当时未采集。每次尝试只保留平台、账号摘要、状态、错误码和等待响应耗时；不记录凭证、原始会话键或消息正文。总调用量仍按一次客户端请求计数，不将内部尝试累加为多次调用。

`attempts[].status` 在有 HTTP 响应时保留其状态；没有响应时按最终失败原因记录。HTTP 200 后流内拒绝仍可能带错误码，不能只看尝试的状态判断整次调用成功。

`/api/live` 的 `phase` 区分 `reading`、`routing`、`queued`、`connecting`、`uploading`、`waiting_headers`、`waiting_output` 和 `streaming`。上传阶段来自 HTTP 写出回调；它描述网关观测阶段，不代表模型内部计算状态。定义和历史兼容见[实时阶段](metrics.md#实时阶段)。流中途返回的错误会保留实际错误码并计为失败；已经开始的响应不自动重放。

网关不缓存模型的完整回答。提示缓存由实际服务商管理，重复请求仍会发送给上游；是否命中应查看返回用量，不能只比较耗时。

- Chat Completions：输入缓存位于 `usage.prompt_tokens_details.cached_tokens`，写入数为同一对象的 `cache_write_tokens`；思考输出位于 `completion_tokens_details.reasoning_tokens`。
- Responses：对应 `usage.input_tokens_details.cached_tokens` / `cache_write_tokens`，以及 `output_tokens_details.reasoning_tokens`。
- Messages：分别返回 `cache_read_input_tokens`、`cache_creation_input_tokens` 和未命中 / 未写入的 `input_tokens`。三者相加才是总输入，不能将缓存数再次加到 Chat / Responses 的总输入上。

流式与非流式使用相同口径，流式真实用量在结束事件补报。`/api/metrics` 的最近调用记录保留 `cached_tokens` / `cache_write_tokens`；旧记录没有字段，无法追补真实历史命中率。

新调用记录的 `usage_reported: true` 表示实际收到上游用量，可据此区分明确零用量与未报告；不应将缺失的缓存字段自动解释为历史零命中。`first_output_ms` 是从请求到达至首段非空文本 / 思考 / 工具参数的等待毫秒数，缺失代表未测得。`gen_ms` 则保留接流之后的接收时长口径，不能将其解释为模型内部解码时长。

OpenRouter 适配器保留客户端显式设置的顶层 / 文本块 / 图片块 / 工具 `cache_control`（含 TTL），以及 `prompt_cache_key`。同一会话的路由键经哈希后传入上游 `session_id`，避免暴露本地用户标识。缓存仍取决于模型、供应端、最小长度和上游策略，网关不会自动为用户开启收费缓存或取消免费模型与零价格路由限制。规则见 [OpenRouter 提示缓存](https://openrouter.ai/docs/guides/best-practices/prompt-caching)。

WorkBuddy、Cursor 与 TRAE 未确认存在可由普通 API 客户端强制控制的缓存断点接口：不要把传入 `cache_control` 当作这些平台已开启缓存的证明。网关维持稳定内容并转回上游实际报告的用量。换账号、换模型 / 供应端、前缀改动或缓存过期，都可能需要重新预热。

## 请求示例

以下是 macOS / Linux shell 示例。把 `YOUR_ROUTING_ID` 换成目录中的完整模型 ID；启用鉴权后将 `agent2api-local` 换成实际网关密钥。

```bash
# Chat Completions
curl http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer agent2api-local' \
  -d '{"model":"YOUR_ROUTING_ID","stream":true,"messages":[{"role":"user","content":"你好"}]}'

# Responses
curl http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer agent2api-local' \
  -d '{"model":"YOUR_ROUTING_ID","input":"你好"}'

# Anthropic Messages
curl http://127.0.0.1:8787/v1/messages \
  -H 'Content-Type: application/json' \
  -H 'x-api-key: agent2api-local' \
  -H 'anthropic-version: 2023-06-01' \
  -d '{"model":"YOUR_ROUTING_ID","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'
```

Windows PowerShell 使用自己的 JSON 与请求语法，避免将 Bash 的反斜线续行直接粘贴过来：

```powershell
$body = @{
  model = 'YOUR_ROUTING_ID'
  messages = @(@{ role = 'user'; content = '你好' })
} | ConvertTo-Json -Depth 10
$headers = @{ Authorization = 'Bearer agent2api-local' }
Invoke-RestMethod -Uri 'http://127.0.0.1:8787/v1/chat/completions' -Method Post -ContentType 'application/json; charset=utf-8' -Headers $headers -Body ([Text.Encoding]::UTF8.GetBytes($body))
```

## 流式与错误

`stream: true` 使用 SSE；前置代理需及时刷新数据，客户端应处理错误事件与流结束。非流式由网关聚合上游事件后返回。网关返回协议对应的错误载荷；上游额度、限流和鉴权错误不会因模型存在于目录而消失。

`/health` 的 `ok` 仅表示本地服务运行，不探测上游。`/metrics` 是 Prometheus 文本格式；控制台还提供真实调用记录。余额查询尚未实现。

## 响应头与错误码

| 响应头 | 含义 |
|---|---|
| `X-Request-ID` | 网关生成的单次逻辑请求编号，可在调用日志中搜索 |
| `X-Upstream-Attempts` | 已使用的生成尝试数，不含目录查询或令牌刷新 |
| `X-Queue-Ms` | 本地容量等待耗时，单位毫秒 |
| `Retry-After` | 已知限流或容量恢复建议；没有依据时不虚构时长 |
| `X-Agent2API-Images-*` | 已启用图片处理策略时的准备 / 省略数量 |

HTTP 响应开始前，错误为 `{"error":{"message":"说明","type":"server_error","code":"机器码"}}`。开始后使用相应协议的流内错误事件；客户端不能只依靠已发出的 HTTP 200 判断成功。

| 典型机器码 / 状态 | 含义 | 下一步 |
|---|---|---|
| `routing_timeout` / 503 | 接受流前的共享等待预算用尽 | 核对上传、尝试及有效预算 |
| `queue_full`、`queue_timeout` / 503 | 本地容量等待超出限制 | 查看并发和排队配置 |
| `retry_budget_exhausted` / 503 | 共享生成尝试数用尽 | 查看每次尝试的实际错误 |
| `upstream_header_timeout` / 504 | 受适配器约束的响应等待超时 | 检查对应上游与超时设置 |
| 限流 / 429 | 模型或账号额度窗口限制 | 遵循已知 `Retry-After`，不能据此推断次数或 Token 制 |
| 鉴权 / 401 | 本地密钥或上游凭证不被接受 | 区分本地访问密钥与账号重新授权 |
| `body_too_large`、上下文超限 / 413 | 传输字节或模型上下文超限 | 分别核对字节上限和模型能力 |
| 日志中的 499 | 客户端停止等待或写回失败 | 属于观测结果，不一定是线路中实际发出的 HTTP 状态 |

上游业务码保留在错误及调用记录中。模糊的 `request illegal` 不能单独证明封号，见[故障排查](troubleshooting.md)。

## 控制台只读接口

以下接口使用网关鉴权，面向本地管理与诊断；字段可能随功能增加。示例使用自己的网关地址和访问密钥，不使用上游 token。

| GET 端点 | 主要内容 |
|---|---|
| `/api/status` | 当前服务、监听地址、版本和已装配平台 |
| `/api/config` | 当前进程有效设置和共享预算，不返回上游密钥 |
| `/api/models` | 平台限定路由、能力、费率来源与目录状态 |
| `/api/metrics` | 累计量、分组、分钟桶、最近 200 条、日历史及存储状态 |
| `/api/live` | 同一快照的在途数、当前请求阶段、接收字符与估算速率 |
| `/api/accounts/manage` | 账号调度和凭证状态；来源与操作能力分平台 |
| `/api/accounts/connect?platform=ID` | POST JSON：`{"action":"check"}` 检测 OpenRouter / Cursor / TRAE；OpenRouter 支持 `{"action":"save_key","api_key":"…"}` 验证并保存单连接密钥；返回模型数，不返回密钥 |
| `/api/accounts/checkin?platform=trae-cn` | GET 返回缓存的余额与签到状态，`refresh=1` 只刷新查询；POST `{"action":"configure","enabled":true,"time":"09:00"}` 保存北京时间的自动签到设置；`{"action":"claim"}` 仅在自动失败且等待间隔结束后补救 |
| `/api/accounts/models` | 账号与模型能力 / 冷却矩阵 |
| `/api/clients/pi`、`/api/clients/cc-switch` | 本机 GET 检测；POST `{"platforms":["workbuddy","trae-cn"],"apps":["claude","codex"]}` 验证所选平台免费权益并备份写入；`apps` 仅用于 cc-switch，可省略为 `claude`。需 JSON 与 `X-Agent2API-Local-Action: pi-sync` / `cc-switch-sync`；返回模型、供应商、新增 / 保留 / 移除及备份摘要。pi 缺失配置可创建，cc-switch 缺失数据库不可创建；拒绝远程访问、任意路径 / URL、并发覆盖与当前供应商失效清理 |
| `/api/clients/zcode` | 本机 GET 检测 ZCode 配置；POST `{}` 重新核验免费目录、备份并增量同步，需 `Content-Type: application/json` 与 `X-Agent2API-Local-Action: zcode-sync`；返回模型与备份摘要，不返回凭据 |

新增字段应兼容缺失值，不能将旧后端未提供的阶段、缓存或测量值默认为零。管理修改、设备登录及重新授权由控制台协调，不能把只读诊断当成登录或额度探测。

`/api/config.platforms[].supports_sanitize` 明确声明该适配器是否实现内容脱敏开关；不支持的平台不应展示可点击的开关。它与是否使用账号池、是否支持目录刷新是不同能力。

累计 / 样本 / 日期范围、缓存分母和隐私边界集中定义在[指标口径](metrics.md)。
