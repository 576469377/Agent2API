# 配置与模型路由

[文档导航](README.md) · [平台支持](platforms.md) · [快速开始](quickstart.md) · [账号管理](accounts.md)

优先级：命令行参数 > 环境变量 > 配置文件 > 内置默认。示例见 [`config.example.json`](../config.example.json)。

配置文件必须用 `-config` 显式指定，不会自动读取工作目录的 `config.json`。`upstream.platforms` 列表优先于单平台字段；已有显式列表时，修改 `-platform` 不会替换该列表。模型顺序、密钥及大部分运行参数在启动时读取，控制台仅对明确支持的项热更新。

## 常用字段与有效值

| JSON 字段 | 默认值 | 含义 / 生效方式 |
|---|---|---|
| `server.host` / `server.port` | `127.0.0.1` / `8787` | 监听入口；重启生效 |
| `server.max_body_mib` | `256` | 客户端请求体字节上限，不是模型上下文；重启生效 |
| `upstream.request_timeout_sec` | `60` | 非流式聚合总超时；OpenRouter / Cursor 还用作建流超时 |
| `upstream.stream_idle_timeout_sec` | `120` | 接流期间空闲上限 |
| `upstream.stream_total_timeout_sec` | `1800` | 一次上游流的总时长上限 |
| `upstream.model_cache_ttl_sec` | `1800` | 模型目录缓存；手动刷新另行失效缓存 |
| `upstream.max_concurrency_per_account` | `0` | 默认不限；正数才启用本地容量与排队 |
| `gateway.max_upstream_attempts` | `6` | 跨重拨、账号与平台的生成尝试总数 |
| `gateway.routing_timeout_sec` | `600` | 排队、上传及接受响应前的总等待预算 |
| `gateway.queue_timeout_sec` | `30` | 所有候选账号满载时的最多排队时间 |
| `gateway.max_queued_requests` | `128` | 每个平台的本地等待人数上限 |
| `auth.api_key` | 空 | 本地网关访问密钥，与上游 API Key 分开 |
| `metrics_file` | 由启动入口推导 | 显式路径优先；默认位置见[运行手册](operations.md#备份与恢复) |

设置页和 `GET /api/config` 显示当前进程的有效预算，不应根据磁盘上的另一份配置推断已生效。热更新的脱敏开关与目录刷新不等于自动写回启动文件；其余配置修改后按[升级步骤](operations.md#更新网关)重启。

## 命令行与环境变量

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
| `-platform` | — | 固定单平台（`workbuddy` / `workbuddy-global` / `openrouter` / `cursor`）；缺省自动集成 WorkBuddy 两区 |

## 多平台配置（upstream.platforms）

想在同一个网关里聚合多个上游平台时，用 `upstream.platforms` 声明：

```json
{
  "upstream": {
    "platforms": [
      { "id": "workbuddy", "accounts_dir": "auths/cn", "sanitize": true },
      { "id": "workbuddy-global", "accounts_dir": "auths/global", "sanitize": true },
      { "id": "openrouter", "api_key_env": "OPENROUTER_API_KEY" },
      { "id": "cursor" }
    ]
  }
}
```

- **路由语义**：裸模型名按配置中的平台顺序选取，同名时优先第一个平台；`workbuddy/模型名` 与 `workbuddy-global/模型名` 固定区域。`/v1/models` 返回唯一的裸模型名和各平台限定名，`/api/models` 提供 `routing_id` 与 `default_route`。未知裸模型名透传默认平台。
- **同名回退**：`models.fallback_on_unavailable: true` 时，裸名在建流前遇到额度不足、限流或 HTTP 5xx，可尝试其他平台目录中的同名模型。默认为关闭；显式平台限定名或指定平台的别名始终固定区域，响应开始后不回退。用 `models.aliases: {"hy3":"workbuddy-global/hy3"}` 可覆盖某个裸名的默认路由。
- **字段回退**：平台内的 `credential_path` / `accounts_dir` / `base_url` / 各超时项缺省时回退到顶层同名配置；平台之间完全隔离（各自独立的号池、冷却与登录会话）
- **`sanitize` 例外**：显式声明 platforms 时，每个平台单独填写 `"sanitize": true` 才会开启内容脱敏；未填视为关闭，不继承顶层值。建议接入客户端时保持开启。
- **零配置（缺省）**：保留国内版与国际版；没有可用凭证的平台保留空账号池与登录入口，完成首次登录后自动入池

## Cursor（个人 Free 额度）

在 `upstream.platforms` 保留或追加 `{"id":"cursor"}`，用**启动网关的同一系统用户**登录最新 Cursor 桌面端，再启动新版网关。不使用配置文件时，可用 `agent2api -platform cursor` 单独启动。当前只发布真实调用验证过的 `cursor/composer-2.5`，不列出 Auto、Max、Fast 或其他未验证模型。Free 有额度限制，耗尽后报错，不转为付费请求。

每次生成前重新读取本机登录态，并从上游检查：个人 / individual 都为 Free、订阅为 unpaid、非团队、非学生套餐、按量付费被明确禁用；状态不完整或发生变化时拒绝调用。目录也会重新检查相同权益。该平台不参与 WorkBuddy 设备码登录或号池。

Windows 从 `%APPDATA%/Cursor/User/globalStorage/state.vscdb` 以只读方式读取访问令牌，使用系统 SQLite，不依赖 Python、不导出凭证；实际版本、commit 和已有设备标识从安装与用户数据读取。不会生成或重置设备标识。macOS / Linux 读取 SQLite 需要 Python 3。自定义位置可使用 `CURSOR_USER_DATA_DIR`（用户数据根目录）、`CURSOR_CLIENT_PATH`（包含 package.json / product.json 的 resources/app 目录）；平台 `credential_path` 可单独指定 SQLite 文件。

三种下游协议支持文本、历史、思考与客户端工具。工具参数通过 API 返回 ZCode 等调用方，网关自身不执行命令、文件或浏览器操作。图片暂不支持。Cursor 保留原生 Agent 系统规则：客户端 `system` 作为明确标注的客户端上下文传递，不能提供与标准模型 API 完全相同的 system 优先级。工具历史使用原生结构化调用与结果，保留 ID、参数和错误状态；工具描述直接传入，避免进入桌面工具描述文件的发现流程。原生 Read 仅在已声明兼容的客户端 Read 时转换为客户端调用，其他未声明的主机操作仍拒绝。

Cursor Agent 不开放采样、effort、JSON 输出模式及 stop 参数，传入这些参数会返回可读错误。ZCode 同步为 Cursor 设置固定的思考选项，不发送 effort 参数。`max_tokens` / `max_output_tokens` 依据上游 token 增量在网关侧截断，默认与最大为 8192，包含思考与工具步骤，可能有一个增量块的误差；工具提前返回时用量只包含已观察的输出，完整回复使用上游结算用量。

诊断命令：`python scripts/diagnose-cursor.py`，只检查账号、计费状态、实际版本与目录，不调用生成。实现依据、实测范围及旧协议失败原因见 [Cursor 接入验证](research/05-Cursor接入调研.md)。

## OpenRouter（仅免费模型）

在现有 `upstream.platforms` 中追加 `{"id":"openrouter","api_key_env":"OPENROUTER_API_KEY"}`。不使用配置文件时，可用 `agent2api -platform openrouter` 单独启动。默认上游为 `https://openrouter.ai/api/v1`，自定义 `base_url` 应包含 `/api/v1`。

从 [OpenRouter 密钥管理](https://openrouter.ai/settings/keys)创建自己的 API Key，然后在启动网关的终端里设置 `OPENROUTER_API_KEY`。PowerShell 示例：

```powershell
$env:OPENROUTER_API_KEY = '<YOUR_OPENROUTER_API_KEY>'
./bin/agent2api.exe -config config.json
```

平台配置也支持 `api_key`；优先使用 `api_key_env` 指定的环境变量（缺省 `OPENROUTER_API_KEY`），避免将密钥写入仓库。上游密钥按平台隔离，不使用 `auth.api_key`，后者只保护本地网关。OpenRouter 使用单个 API Key 直连，不参与 WorkBuddy 设备码登录、凭证目录或多账号号池调度。

适配器从官方 `/models` 动态获取目录，要求输入、输出以及所有已发布的额外价格均为零；收费、动态价格、价格缺失及无法解析的条目不会暴露或允许调用。请求再次校验免费目录，并发送 `provider.max_price` 的输入、输出、请求、图片零价格限制；目录过期后若刷新失败，拒绝生成。不会删去 `:free` 后缀或回退到付费版本。

模型路由保留 OpenRouter 原始作者前缀，例如 `openrouter/作者/模型:free`。官方免费随机路由若在免费目录中，会显示为 `openrouter/openrouter/free`；其输出上限缺失时，ZCode 同步会跳过。三种下游协议共用此适配器，支持文本、图片输入、工具调用、思考输出和用量聚合。官方 API 不需要 WorkBuddy 的内容脱敏。

免费模型仍有频率、每日额度与可用性限制；增加 API Key 不会提高同账号限额。接入依据：[模型 API](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties)、[免费变体](https://openrouter.ai/docs/guides/routing/model-variants/free)、[零价格路由限制](https://openrouter.ai/docs/guides/routing/provider-selection#max-price)、[官方限额](https://openrouter.ai/docs/api_reference/limits)。

子命令：

| 命令 | 用途 |
|---|---|
| `agent2api` | 启动网关 |
| `agent2api login` | 设备码登录 |
| `agent2api models` | 列出可用模型（多平台时合并列出，`-platform` 可只看某一个） |
| `agent2api dedupe` | 清理凭证目录里的重复与无效凭证；默认只预览，加 `-yes` 实际删除 |

> [!NOTE]
> `server.write_timeout_sec` 与 `log.level` / `log.format` 目前不会被读取（流式响应刻意不设写超时；日志级别/格式尚未实现），仅为向后兼容保留。

## 单请求图片数量护栏（models.image_limits）

HTTP 请求体默认上限为 **256 MiB**，可通过 `server.max_body_mib` 调整；填 `0` 使用默认值。它限制包含 Base64 图片的传输字节，与模型的 token 上下文窗口独立，不会压缩图片或删除历史。超过上限返回 HTTP **413**、错误码 `body_too_large`；请求体读取失败则返回 HTTP 400、`body_read_failed`。修改后需要重启网关。

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

## 旧对话超限后的续聊策略（models.image_overflow_policies）

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

## 模型别名（models.aliases）

客户端可能发送不在上游模型目录中的名字。别名表把它们映射到账号实际可用的模型上，
客户端**无需改动原有模型名**即可接入：

```json
{
  "models": {
    "aliases": {
      "local-free": "workbuddy/hy3",
      "local-free-global": "workbuddy-global/hy3"
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
- 示例只使用本机观察为免费的目标；使用前仍须刷新目录确认。默认示例不预设付费模型别名。

## 并发上限（max_concurrency_per_account）

```json
{ "upstream": { "max_concurrency_per_account": 0 } }
```

默认不限制每账号并发。需要本地排队时，可显式设置正数上限；选号时优先挑还有余量的账号：

- 默认 **0（不限并发）**；只有显式设置正数才启用上限。已有配置中的正数不会自动被默认值覆盖。
- 这是**背压**不是失败：排队期间客户端连接保持，不会被换号或标记冷却
- 排队只发生在所有账号都满员时——只要有账号空闲，请求立刻走它
- 平台级可用 `upstream.platforms[].max_concurrency_per_account` 的正数单独覆盖；平台级 `0` 表示继承顶层值。要关闭并发限制，将顶层设为 `0`，对应平台不填写正数覆盖值。
- 此设置在后端启动时读取，修改配置文件后需要重启后端；刷新控制台不改变运行中的上限。控制台显示实际运行值，`不限` 表示没有本地并发上限。

## 请求预算和等待队列（gateway）

```json
{
  "gateway": {
    "max_upstream_attempts": 6,
    "routing_timeout_sec": 600,
    "queue_timeout_sec": 30,
    "max_queued_requests": 128
  }
}
```

- `max_upstream_attempts` 是一次客户端请求跨连接重试、账号和平台的生成请求总预算，默认 6 次；配置查询和凭证刷新不消耗此预算。取值 1–64，0 使用默认值。
- `routing_timeout_sec` 从请求解码完成开始限制选路、排队、请求上传和建流等待，默认 600 秒。图片历史的上传与长上下文预处理也占用此预算，因此默认值留足大请求等待时间；显式配置的较小值仍会生效。上游流被接受后停止此计时，不截断正常长回答；生成期间仍使用原有空闲和总时长上限。非流式的原有请求总超时仍可更早生效。
- WorkBuddy 流式生成不使用 `request_timeout_sec`（默认 60 秒）截断上传或建流，改由上述共享预算和 `stream_total_timeout_sec` 约束。`request_timeout_sec` 仍控制非流式聚合请求总时长，以及 OpenRouter / Cursor 的建流等待。
- `queue_timeout_sec` 默认最多排队 30 秒，`max_queued_requests` 默认每个平台最多等待 128 条。只在显式设置正数账号并发上限、且候选账号全部满载时排队；默认的不限并发设置保持不变。
- 选号与占槽在同一临界区完成；任意账号释放容量时重新选号，取消、结束、建流失败都释放资源。本地队列或预算不足返回明确的 HTTP 503，不将账号标成额度耗尽或鉴权失效。
- 两项秒数范围为 1–86400，等待人数为 1–100000；0 均表示使用默认值。这些设置在启动时读取，可从 `/api/config` 查看当前生效值。

这些预算保护的是不同阶段。遇到长请求失败时，先查看调用详情中的 `error_code`、`attempts`、排队与首段等待，不要把每个超时统一处理成“增加非流式超时”。客户端或前置代理先停止等待，仍可能导致取消，见[故障排查](troubleshooting.md#长请求与超时)。

## Prometheus 指标（/metrics）

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
