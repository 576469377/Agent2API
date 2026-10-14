# 配置与模型路由

[文档导航](README.md) · [快速开始](quickstart.md) · [账号管理](accounts.md)

优先级：命令行参数 > 环境变量 > 配置文件 > 内置默认。示例见 [`config.example.json`](../config.example.json)。

配置文件必须用 `-config` 显式指定，不会自动读取工作目录的 `config.json`。`upstream.platforms` 列表优先于单平台字段；已有显式列表时，修改 `-platform` 不会替换该列表。模型顺序、密钥及大部分运行参数在启动时读取，控制台仅对明确支持的项热更新。

| 参数 | 环境变量 | 说明 |
|---|---|---|
| `-config` | — | 配置文件路径（JSON） |
| `-port` | `AGENT2API_PORT` | 监听端口，默认 8787 |
| `-host` | `AGENT2API_HOST` | 监听地址，默认 127.0.0.1 |
| `-api-key` | `AGENT2API_API_KEY` | 网关访问密钥，为空则不鉴权 |
| `-credential` | `AGENT2API_CREDENTIAL_PATH` | 凭证文件路径，为空时自动探测 |
| `-accounts-dir` | — | 号池目录，扫描其中具有有效凭证结构的 `*.json`，其他 JSON 跳过 |
| `-base-url` | `AGENT2API_BASE_URL` | 上游地址 |
| `-metrics-file` | — | 指标落盘路径；缺省写配置文件同目录的 `metrics.json`，无配置文件时写工作目录的 `agent2api-metrics.json` |
| `-no-persist` | — | 关闭指标落盘 |
| `-no-sanitize` | — | 关闭内容脱敏 |
| `-platform` | — | 固定单平台（`workbuddy` 国内版 / `workbuddy-global` 国际版）；缺省自动集成全部内置平台 |

### 多平台配置（upstream.platforms）

想在同一个网关里聚合多个上游平台时，用 `upstream.platforms` 声明：

```json
{
  "upstream": {
    "platforms": [
      { "id": "workbuddy", "accounts_dir": "auths/cn", "sanitize": true },
      { "id": "workbuddy-global", "accounts_dir": "auths/global", "sanitize": true }
    ]
  }
}
```

- **路由语义**：裸模型名按配置中的平台顺序选取，同名时优先第一个平台；`workbuddy/模型名` 与 `workbuddy-global/模型名` 固定区域。`/v1/models` 返回唯一的裸模型名和各平台限定名，`/api/models` 提供 `routing_id` 与 `default_route`。未知裸模型名透传默认平台。
- **同名回退**：`models.fallback_on_unavailable: true` 时，裸名在建流前遇到额度不足、限流或 HTTP 5xx，可尝试其他平台目录中的同名模型。默认为关闭；显式平台限定名或指定平台的别名始终固定区域，响应开始后不回退。用 `models.aliases: {"glm-5.3":"workbuddy-global/glm-5.3"}` 可覆盖某个裸名的默认路由。
- **字段回退**：平台内的 `credential_path` / `accounts_dir` / `base_url` / 各超时项缺省时回退到顶层同名配置；平台之间完全隔离（各自独立的号池、冷却与登录会话）
- **`sanitize` 例外**：显式声明 platforms 时，每个平台单独填写 `"sanitize": true` 才会开启内容脱敏；未填视为关闭，不继承顶层值。建议接入客户端时保持开启。
- **零配置（缺省）**：保留国内版与国际版；没有可用凭证的平台保留空账号池与登录入口，完成首次登录后自动入池

子命令：

| 命令 | 用途 |
|---|---|
| `agent2api` | 启动网关 |
| `agent2api login` | 设备码登录 |
| `agent2api models` | 列出可用模型（多平台时合并列出，`-platform` 可只看某一个） |
| `agent2api dedupe` | 清理凭证目录里的重复与无效凭证；默认只预览，加 `-yes` 实际删除 |

> [!NOTE]
> `server.write_timeout_sec` 与 `log.level` / `log.format` 目前不会被读取（流式响应刻意不设写超时；日志级别/格式尚未实现），仅为向后兼容保留。

### 单请求图片数量护栏（models.image_limits）

网关在每次实际平台调用之前统计请求中的图片内容块，包括历史消息及 `tool_result.content` 里的图片。图片数据不会被解析、裁剪或替换；超限时返回 HTTP 400，错误码为 `image_count_exceeded`，提示目标平台 / 模型、当前张数、护栏值以及开新对话、换模型或减少历史图片。

内置表来自用户提供的 2026-10-13/14 真实请求实测，维护位置为 [`internal/config/image_limits.go`](../internal/config/image_limits.go)，并非上游长期承诺：

| 平台 / 模型 | 内置护栏 | 实测边界 |
|---|---:|---|
| `workbuddy-global/hy3` | 200 张 | 200 受理，201 拒绝 |
| `workbuddy-global/step-5-preview` | 60 张 | 60 受理，61 拒绝 |
| `workbuddy-global/deepseek-v4.1-flash` | 未配置 | 实测 248 张受理，不能推断无限制 |
| `workbuddy/*` | 未配置 | 实测 201 张受理，不能套用国际版护栏或推断无限制 |

可在 `config.json` 中按**实际平台 / 模型 ID**覆盖。例如以下配置将国际版 Hy3 护栏改为 180 张，保留未提及的 Step 内置值：

```json
{
  "models": {
    "image_limits": {
      "workbuddy-global/hy3": 180
    }
  }
}
```

正整数启用 / 覆盖该模型护栏；`0` 显式关闭该模型的网关护栏，但不代表上游无限制；缺少的键保留内置值；没有内置值且未配置的模型不做数量拦截。负数、非整数或缺少平台前缀的键会导致配置加载失败。同名国内模型不受国际配置影响。别名使用解析后的目标，跨平台回退检查实际目标平台的值。

上游变化后，可以先覆盖本地配置并记录实测依据，再更新内置表、日期与边界测试。此配置在新版后端启动时读取，控制台未提供热修改；正在使用的反代应在合适时段更新，不会因只刷新网页而生效。

### 旧对话超限后的续聊策略（models.image_overflow_policies）

默认 `reject` 保留完整请求并拒绝超限。ZCode 的「继续」会再次附带同一份历史图片，因此默认模式下仍会超限。如果接受模型不再看到最旧图片，可以显式为目标模型启用 `trim_oldest`：

```json
{
  "models": {
    "image_overflow_policies": {
      "workbuddy-global/hy3": "trim_oldest",
      "workbuddy-global/step-5-preview": "trim_oldest"
    }
  }
}
```

把此字段合并到已有 `models`，保留其他配置。开启后，每次请求超过该模型已启用的图片护栏时，按请求中的时间 / 内容顺序省略最旧图片，仅向上游保留最近的 200 / 60 张（或本地覆盖值）。它只改变本次上游请求，不修改 ZCode 的对话记录、磁盘图片或凭证；再发「继续」时仍会从原历史独立处理，不会再次被相同图片数量卡住。

原有文字、工具调用 / 结果及关联 ID 保留，省略的图片替换为文字标记。模型无法查看已省略图片的细节，需要时应重新附图或通过工具重新读取。策略按完整平台 / 模型隔离，未知或关闭护栏的模型不自动清理；当前一条消息独自包含超限图片时，也按顺序保留最近图片。清理不保证修复坏图、额度或其他上游错误。

控制台记录原始数量、处理后数量、省略数量与策略；模型元数据的 `image_limit.overflow_policy` 也会显示策略。清理后的响应增加 `X-Agent2API-Images-Removed`、`X-Agent2API-Images-Forwarded` 和 `X-Agent2API-Image-Overflow-Policy`，请求日志包含 `images_forwarded` / `images_removed` / `image_policy`。解码计数与原始请求不一致时返回 `image_cleanup_unavailable`，不猜测删除哪些内容。策略在后端启动时读取。

### 模型别名（models.aliases）

客户端可能发送不在上游模型目录中的名字。别名表把它们映射到账号实际可用的模型上，
客户端**无需改动原有模型名**即可接入：

```json
{
  "models": {
    "aliases": {
      "claude-sonnet-4-5-20250929": "glm-5.3",
      "gpt-4o": "workbuddy/deepseek-v4-pro"
    }
  }
}
```

- 两种写法：`"目标模型"`（使用该目标模型的默认路由）或 `"平台ID/目标模型"`（换平台 + 换模型）
- 别名会出现在 `/v1/models` 与控制台模型页（客户端通常只认列表里出现过的名字）
- **不递归**（别名指向的必须是真实模型）；平台名写错时退化为普通模型名并告警，
  不让一个配置笔误把网关变成不可用
- 别名只影响路由与发往上游的模型名：会话亲和、号池调度、脱敏都不变
- 调用日志会同时记录「请求的模型」与「实际使用的模型」，便于核对映射

### 并发上限（max_concurrency_per_account）

```json
{ "upstream": { "max_concurrency_per_account": 0 } }
```

默认不限制每账号并发。需要本地排队时，可显式设置正数上限；选号时优先挑还有余量的账号：

- 默认 **0（不限并发）**；只有显式设置正数才启用上限。已有配置中的正数不会自动被默认值覆盖。
- 这是**背压**不是失败：排队期间客户端连接保持，不会被换号或标记冷却
- 排队只发生在所有账号都满员时——只要有账号空闲，请求立刻走它
- 平台级可用 `upstream.platforms[].max_concurrency_per_account` 的正数单独覆盖；平台级 `0` 表示继承顶层值。要关闭并发限制，将顶层设为 `0`，对应平台不填写正数覆盖值。
- 此设置在后端启动时读取，修改配置文件后需要重启后端；刷新控制台不改变运行中的上限。控制台显示实际运行值，`不限` 表示没有本地并发上限。

### Prometheus 指标（/metrics）

`/metrics` 用 Prometheus 文本格式暴露与控制台同源的指标，指标名与类型行齐全
（`agent2api_requests_total`、`agent2api_in_flight_requests`、按模型/协议/账号的分组计数、
`agent2api_tokens_*_total` 等），另含 `agent2api_build_info{version,go}` 便于按版本对比。

启用 API Key 时抓取端需带上凭据：

```yaml
scrape_configs:
  - job_name: agent2api
    authorization:
      credentials: <api_key>
    static_configs:
      - targets: ["127.0.0.1:8787"]
```

> 无 TPS 样本时**不输出**平均解码速度指标——0 tok/s 与「没测过」是两回事，
> 输出 0 会让告警误报。
