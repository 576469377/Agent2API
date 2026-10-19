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

`/api/models` 还会为已知护栏返回 `image_limit`（扁平目录和分平台目录均提供）：`max_images`、`enabled`、`source`（`builtin_empirical` / `config`），内置实测还包含 `observed_on`。未配置时省略该字段，不代表上游无限制；`max_images: 0, enabled: false` 表示本地显式关闭护栏。它描述的是网关当前有效规则，详见[图片护栏配置](configuration.md#单请求图片数量护栏modelsimage_limits)。

调用记录中的 `image_count` 是原请求图片内容块总数，含历史与工具结果；`max_images` 是当次实际平台的护栏值。旧记录没有这些字段，不应解释为 0 张。默认 `reject` 模式下，超限请求在上游调用前返回 `image_count_exceeded` / HTTP 400，并且不裁剪内容。

显式启用 `models.image_overflow_policies` 的 `trim_oldest` 时，已知超限请求改为省略最旧图片并继续转发；默认 `reject` 不变。`image_limit.overflow_policy` 提供有效策略；记录另含 `forwarded_image_count`（准备发往最后实际平台的数量）、`removed_image_count`、`image_overflow_policy`。清理响应头为 `X-Agent2API-Images-Removed` / `X-Agent2API-Images-Forwarded` / `X-Agent2API-Image-Overflow-Policy`，本地对话未被修改。护栏配置、行为和代价见[旧对话续聊策略](configuration.md#旧对话超限后的续聊策略modelsimage_overflow_policies)。

## 提示缓存与用量

网关不缓存模型的完整回答。提示缓存由实际服务商管理，重复请求仍会发送给上游；是否命中应查看返回用量，不能只比较耗时。

- Chat Completions：输入缓存位于 `usage.prompt_tokens_details.cached_tokens`，写入数为同一对象的 `cache_write_tokens`；思考输出位于 `completion_tokens_details.reasoning_tokens`。
- Responses：对应 `usage.input_tokens_details.cached_tokens` / `cache_write_tokens`，以及 `output_tokens_details.reasoning_tokens`。
- Messages：分别返回 `cache_read_input_tokens`、`cache_creation_input_tokens` 和未命中 / 未写入的 `input_tokens`。三者相加才是总输入，不能将缓存数再次加到 Chat / Responses 的总输入上。

流式与非流式使用相同口径，流式真实用量在结束事件补报。`/api/metrics` 的最近调用记录保留 `cached_tokens` / `cache_write_tokens`；旧记录没有字段，无法追补真实历史命中率。

新调用记录的 `usage_reported: true` 表示实际收到上游用量，可据此区分明确零用量与未报告；不应将缺失的缓存字段自动解释为历史零命中。`first_output_ms` 是从请求到达至首段非空文本 / 思考 / 工具参数的等待毫秒数，缺失代表未测得。`gen_ms` 则保留接流之后的接收时长口径，不能将其解释为模型内部解码时长。

OpenRouter 适配器保留客户端显式设置的顶层 / 文本块 / 图片块 / 工具 `cache_control`（含 TTL），以及 `prompt_cache_key`。同一会话的路由键经哈希后传入上游 `session_id`，避免暴露本地用户标识。缓存仍取决于模型、供应端、最小长度和上游策略，网关不会自动为用户开启收费缓存或取消免费模型与零价格路由限制。规则见 [OpenRouter 提示缓存](https://openrouter.ai/docs/guides/best-practices/prompt-caching)。

WorkBuddy 与 Cursor 未确认存在可由普通 API 客户端强制控制的缓存断点接口：不要把传入 `cache_control` 当作这些平台已开启缓存的证明。网关维持稳定内容并转回上游实际报告的用量。换账号、换模型 / 供应端、前缀改动或缓存过期，都可能需要重新预热。

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
