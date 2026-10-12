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

支持文本、思考过程、工具调用、多轮对话、系统提示词、采样参数、图片输入。

管理与调试界面在 `/`（见[网页控制台](console.md)），不属于 API。

## 鉴权

配置 `auth.api_key` 后，生成接口、模型列表、管理接口和 `/metrics` 需要网关访问密钥。Chat / Responses 通常使用 `Authorization: Bearer <网关密钥>`，Messages 通常使用 `x-api-key: <网关密钥>`；网关支持这两种形式。`/health` 和静态控制台不要求密钥。

## 模型目录

`GET /v1/models` 返回唯一裸名及各平台限定名。`GET /api/models` 提供完整元数据：`platform`、`routing_id`、`default_route`、上下文窗口、输出上限与能力。客户端配置推荐使用 `routing_id`。模式入口的名字不代表某个固定底层模型；没有元数据时不要猜测窗口大小。

新版本的 WorkBuddy 目录还包含 `pricing`：`status`（`free` 表示零积分倍率、`credits`、`dynamic`、`unknown`）、可选的 `credit_multiplier`、`upstream_label`、`source` 和 `observed_at`。来源为上游模型配置，按平台区分，不代表账号余额或金额单价；缺失倍率不能当作零。其他适配器或内置兜底目录可省略此字段。详见[控制台费率说明](console.md#模型费率)。

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
