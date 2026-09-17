# Agent2API

把 AI Agent 平台的私有协议翻译成标准 LLM API 的本地反向代理网关。

Claude Code、Codex CLI 及任意 OpenAI / Anthropic 客户端**零改造**接入：对外提供 Chat Completions、Responses、Anthropic Messages 三种协议，内置多平台枢纽（Hub）按模型名把请求路由到对应上游 —— 当前内置平台为 **WorkBuddy / CodeBuddy**。

[简体中文](README.md) · [English](README.en.md) · [更新日志](CHANGELOG.md) · [免责声明](DISCLAIMER.md)

> [!WARNING]
> **个人学习与研究项目，非生产软件。** 仅供本人已授权账号在本机或私有环境自用，风险自担。
> 它会读取桌面客户端已登录的**凭证**（凭证 = 账号，切勿外传），运作方式**可能不符合上游服务条款**。作者不鼓励也不支持商用或对外提供服务。

## 目录

**上手**：[它解决什么问题](#它解决什么问题) · [快速开始](#快速开始) · [网页控制台](#网页控制台) · [多账号号池](#多账号号池)

**参考**：[支持的接口](#支持的接口) · [配置](#配置) · [安全须知](#安全须知) · [架构](#架构)

**其他**：[测试](#测试) · [常见问题](#常见问题) · [已知限制](#已知限制) · [路线图](#路线图) · [贡献](#贡献) · [许可证](#许可证)

---

## 它解决什么问题

Claude Code、Codex CLI 这类客户端只认 OpenAI / Anthropic 标准协议，而 AI Agent 平台的上游都是私有协议 —— 于是你手上的平台额度，用不到那些好用的客户端里。

本项目在本机做一层协议翻译：不伪造协议，只把标准协议翻译成上游私有协议。

```
Claude Code / Codex / 任意 OpenAI 客户端
        │  /v1/chat/completions · /v1/responses · /v1/messages
        ▼
   ┌────────────────────────────────────────┐
   │  Agent2API                             │
   │  下游协议层 ── IR 层 ── 上游适配层      │
   └────────────────────────────────────────┘
        │  https://copilot.tencent.com/v2/chat/completions
        ▼
   WorkBuddy 上游
```

多平台时，Hub 按 `model` 字段把请求路由到拥有该模型的上游（详见[多平台配置](#多平台配置upstreamplatforms)）。

**核心特性**

- **三种协议一次做全**：Chat Completions / Responses / Anthropic Messages，流式与非流式都支持
- **思考过程**：上游 `reasoning_content` 透传为各协议的思考输出
- **工具调用**：原生 `tool_calls` 通道，含分片参数重组
- **凭证复用**：直接读桌面客户端已登录的凭证，无需重新登录
- **多账号号池**：额度不够用就多放几个账号，按健康度自动调度、失败自动换号
- **多平台枢纽**：`upstream.platforms` 声明多个上游平台，同进程共存、按模型名路由；零配置时自动集成全部内置平台
- **内容脱敏**：避免客户端模板用语被上游关键词审核误伤（接 Claude Code / Codex 必需）
- **内置控制台**：编进单个二进制，无前端构建步骤，中英文与亮暗主题

---

## 快速开始

```bash
# 方式一：直接安装（需 Go 1.23+）
go install github.com/576469377/Agent2API/cmd/agent2api@latest

# 方式二：从源码构建
git clone https://github.com/576469377/Agent2API && cd Agent2API
make build                     # 产出 bin/agent2api
```

**启动不需要任何配置。** 它会自动探测本机桌面客户端的登录态；没装过桌面客户端时，用 `agent2api login` 走设备码登录。

```bash
agent2api                      # 启动，默认 127.0.0.1:8787
agent2api models               # 列出当前账号可用模型
```

启动后命令行会打印控制台地址，浏览器打开即可管理。也可以用一条命令确认网关活着：

```bash
curl -s http://127.0.0.1:8787/health
# {"platform":"workbuddy","platforms":["workbuddy"],"status":"ok","time":…}
```

> [!WARNING]
> **监听 `0.0.0.0` 且未设 `-api-key`，同网段任何人都能用你的账号额度。**

### 接入客户端

**Claude Code**

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
export ANTHROPIC_API_KEY=任意值     # 未配置网关密钥时不会校验
```

**Codex CLI**：base_url 填 `http://127.0.0.1:8787`，API key 随意填（走 `/v1/responses`）。

**任意 OpenAI SDK**：base_url 设为 `http://127.0.0.1:8787/v1`。

> [!TIP]
> 接 Claude Code / Codex 时**不要关闭脱敏**：它们的 system 模板含大量安全声明用语，会被上游审核误判拦截。

### 调用示例

同一句话，三种协议各调一次：

```bash
# OpenAI Chat Completions
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}]}'

# Anthropic Messages
curl http://127.0.0.1:8787/v1/messages \
  -H "Content-Type: application/json" -H "anthropic-version: 2023-06-01" \
  -d '{"model":"deepseek-v4-flash","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'

# OpenAI Responses
curl http://127.0.0.1:8787/v1/responses \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","input":"你好"}'
```

> [!NOTE]
> 把示例里的 `deepseek-v4-flash` 换成 `agent2api models` 列出的任意模型即可；未知模型名会原样透传给默认平台，由上游决定如何处理。

---

## 网页控制台

控制台用 `go:embed` 编进二进制：无前端构建、无 CDN 依赖（原生 SVG 图表）。启动后访问 `http://127.0.0.1:8787`。

| 页面 | 内容 |
|---|---|
| **概览** | 状态墙、请求趋势、Token 消耗、协议/模型/账号排行；带倒计时的自动刷新 |
| **账号** | 号池管理：调度与登录状态、凭证有效期、每账号用量；启停、清冷却、重新登录、添加账号 |
| **对话** | 直接与网关对话，验证协议与模型表现 |
| **平台** | 上游地址、账号数、模型数、健康状态 |
| **模型** | 模型清单与能力标签，可切换账号 × 模型矩阵（列头显示每账号**在途/并发上限**与整号冷却倒计时） |
| **调用日志** | 最近 200 条请求（标注实际使用的账号），失败行高亮 |
| **设置** | 脱敏开关（热更新）、重新拉取模型清单、访问密钥、当前生效配置 |

> 仅对话页的**公式渲染**会在首次遇到数学公式时从 CDN 加载 KaTeX，离线时回退为等宽原文，不影响其他功能。

---

## 多账号号池

一个账号额度不够用？把多个凭证放进号池目录，网关自动调度、失败自动换号。

```bash
agent2api login -out auths/account-a.json    # 逐个登录
agent2api login -out auths/account-b.json
agent2api -accounts-dir auths                 # 启动（工作目录有 auths/ 时自动启用）
```

也可以在控制台「账号」页直接添加账号（设备码登录）：凭证自动落盘，热加载每 10 秒扫描一次目录，无需重启。

### 调度语义

| 情况 | 行为 |
|---|---|
| 日常调度 | 按账号**健康度加权随机**选号，健康者更易被选中但不被独占；同一会话**粘住同一账号**，长上下文不在账号间漂移 |
| 并发压力 | 每账号有**并发上限**（默认 4，`max_concurrency_per_account` 可调）：达到上限的账号不再被选中，请求**排队等槽位**而不是失败（本地背压）。在途数在控制台矩阵列头显示为「2/4」 |
| 限流（429）且上游给出精确重置时刻 | 只冷却**该账号上的该模型**到指定时刻（封顶 24h）并换号，同账号其他模型不受影响 |
| 限流（429）但无精确重置时刻 | **不锁定**：只降低该账号健康度并换号 —— 避免一次普通限流把账号/模型整体挂起（实测曾出现「一个问题问完，两个号都冷却」） |
| 额度耗尽（14018） | 同样**不锁定**：上游的 credits 是积分倍率而非账号余额，且上游不提供恢复时刻；降健康度换号，同账号的免费/低价模型照常可用 |
| 鉴权失效（刷新后仍 401） | 整号标记 **blocked**（终态，不会自动恢复）—— 这是账号死亡而非限流，反复重试无意义；在控制台「重新登录」或「清除冷却」后恢复 |
| 参数错误（如上下文超限） | 直接失败 —— 换任何账号结果都一样 |
| 传输错误 / 5xx | 换号但不冷却（可能是全局抖动），池级退避加抖动 |
| 该模型全池不可用 | 返回 429 并附**最早**解冻秒数，提示改用其他模型；若全部是鉴权失效则返回 401（重试永远不会成功） |

健康分 = 成功率 EWMA×0.6 + 延迟 EWMA×0.4，连续失败逐次降权，样本少时向中性收缩。精确参数见 [`internal/adapter/pool.go`](internal/adapter/pool.go)。

控制台「账号」页可运行时干预，无需重启网关：

- **停用 / 启用**：临时把某账号摘出调度
- **清除冷却**：上游提前恢复时手动解冻（也是 blocked 账号的恢复入口）
- **重新登录**：掉线账号直接发起设备码登录，凭证写回原文件
- 每账号的调度状态、登录状态、凭证有效期与用量（落盘持久化）

> 桌面客户端凭证与号池文件可以并存，账号卡片会标注来源。桌面客户端**下线不影响**网关运行 —— 凭证只是启动时读一次，后续刷新由网关自己完成。

---

## 支持的接口

| 接口 | 协议 | 流式 | 非流式 |
|---|:-:|:-:|:-:|
| `POST /v1/chat/completions` | OpenAI Chat Completions | ✅ | ✅ |
| `POST /v1/responses` | OpenAI Responses | ✅ | ✅ |
| `POST /v1/messages` | Anthropic Messages | ✅ | ✅ |
| `GET /v1/models` | 模型列表（含 `models.aliases` 声明的别名） | — | ✅ |
| `GET /metrics` | Prometheus 文本格式指标（与控制台指标同源，需 API Key） | — | ✅ |
| `GET /health` | 健康检查（只报状态与平台 ID，不探测上游） | — | ✅ |

支持文本、思考过程、工具调用、多轮对话、系统提示词、采样参数、图片输入。

管理与调试界面在 `/`（见[网页控制台](#网页控制台)），不属于 API。

---

## 配置

优先级：命令行参数 > 环境变量 > 配置文件 > 内置默认。示例见 [`config.example.json`](config.example.json)。

| 参数 | 环境变量 | 说明 |
|---|---|---|
| `-config` | — | 配置文件路径（JSON） |
| `-port` | `AGENT2API_PORT` | 监听端口，默认 8787 |
| `-host` | `AGENT2API_HOST` | 监听地址，默认 127.0.0.1 |
| `-api-key` | `AGENT2API_API_KEY` | 网关访问密钥，为空则不鉴权 |
| `-credential` | `AGENT2API_CREDENTIAL_PATH` | 凭证文件路径，为空时自动探测 |
| `-accounts-dir` | — | 号池目录，目录下每个 `*.json` 视为一个账号 |
| `-base-url` | `AGENT2API_BASE_URL` | 上游地址 |
| `-metrics-file` | — | 指标落盘路径；缺省写配置文件同目录的 `metrics.json`，无配置文件时写工作目录的 `agent2api-metrics.json` |
| `-no-persist` | — | 关闭指标落盘 |
| `-no-sanitize` | — | 关闭内容脱敏 |
| `-platform` | — | 固定单平台（当前仅 `workbuddy`）；缺省自动集成全部内置平台 |

### 多平台配置（upstream.platforms）

想在同一个网关里聚合多个上游平台时，用 `upstream.platforms` 声明：

```json
{
  "upstream": {
    "platforms": [
      { "id": "workbuddy", "accounts_dir": "auths", "sanitize": true }
    ]
  }
}
```

- **路由语义**：`/v1/models` 合并所有平台的模型清单（每项带 `platform` 归属）；请求的 `model` 精确命中哪个平台的目录就路由到哪个平台，**未命中则透传给默认平台**（配置列表的第一个）——保持单平台时代「未知模型名交给上游」的语义
- **字段回退**：平台内的 `credential_path` / `accounts_dir` / `base_url` / 各超时项缺省时回退到顶层同名配置；平台之间完全隔离（各自独立的号池、冷却与登录会话）
- **⚠️ `sanitize` 例外**：布尔值无法区分「未填」与「false」，显式声明 platforms 时**每个平台都要显式写 `"sanitize": true`**（接 Claude Code / Codex 必需），漏写该平台会裸奔
- **零配置（缺省）**：自动集成全部内置平台（当前即 workbuddy），能登录哪个就用哪个；探测不到的平台跳过并告警

子命令：

| 命令 | 用途 |
|---|---|
| `agent2api` | 启动网关 |
| `agent2api login` | 设备码登录 |
| `agent2api models` | 列出可用模型（多平台时合并列出，`-platform` 可只看某一个） |
| `agent2api dedupe` | 清理凭证目录里的重复与无效凭证；默认只预览，加 `-yes` 实际删除 |

> [!NOTE]
> `server.write_timeout_sec` 与 `log.level` / `log.format` 目前不会被读取（流式响应刻意不设写超时；日志级别/格式尚未实现），仅为向后兼容保留。

### 模型别名（models.aliases）

客户端会发固定的模型名（Claude Code 发 `claude-sonnet-4-5-*`、Codex 发 `gpt-5-*`），
而这些名字常常不在账号的可用目录里。别名表把它们映射到账号实际可用的模型上，
客户端**零改动**即可接入：

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

- 两种写法：`"目标模型"`（同平台换模型）或 `"平台ID/目标模型"`（换平台 + 换模型）
- 别名会出现在 `/v1/models` 与控制台模型页（客户端通常只认列表里出现过的名字）
- **不递归**（别名指向的必须是真实模型）；平台名写错时退化为普通模型名并告警，
  不让一个配置笔误把网关变成不可用
- 别名只影响路由与发往上游的模型名：会话亲和、号池调度、脱敏都不变
- 调用日志会同时记录「请求的模型」与「实际使用的模型」，便于核对映射

### 并发上限（max_concurrency_per_account）

```json
{ "upstream": { "max_concurrency_per_account": 4 } }
```

客户端（Claude Code 等）会并发发请求，同一账号上同时压着十几个请求时上游极易回 429。
并发上限把请求**排在账号前面**（本地排队，受客户端超时约束），选号时优先挑还有余量的账号：

- 默认 **4**；设 `0` 关闭（不限并发）
- 这是**背压**不是失败：排队期间客户端连接保持，不会被换号或标记冷却
- 排队只发生在所有账号都满员时——只要有账号空闲，请求立刻走它
- 平台级可用 `upstream.platforms[].max_concurrency_per_account` 单独覆盖

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

---

## 安全须知

1. **凭证来源**：网关不要求输入账号密码，而是读取本机桌面客户端已登录的凭证文件 —— 等价于把桌面端登录态借给本机进程。
2. **凭证文件 = 你的账号**：请勿拷贝到其他机器、勿提交进仓库。`.gitignore` 已忽略相关路径。
3. **默认只监听 `127.0.0.1`**：改为 `0.0.0.0` 且未设 `-api-key` 时，同网段任何人可消耗你的额度。共享环境必须设 `-api-key`，并在前置反向代理做鉴权。
4. **日志与指标**可能记录模型名与 token 数；`-no-persist` 可关闭落盘。

漏洞报告见 [SECURITY.md](SECURITY.md)。

---

## 架构

```
internal/
├── llm/          IR 层（零依赖）：请求/响应/错误的中间表示
├── adapter/      上游平台接缝：Adapter 接口
│   └── workbuddy/  WorkBuddy 实现：认证 / 请求 / SSE / 模型 / 脱敏
├── api/          下游协议编解码
│   ├── common/     SSE 写出、错误载荷（三协议共用）
│   ├── openai/chat/ · openai/responses/ · anthropic/messages/
├── app/          HTTP 路由与编排（hub：多平台运行时）
├── obs/          指标采集与持久化
├── web/          内置控制台（go:embed）
└── config/       配置
```

**设计要点**：`api/*` 与 `adapter/*` 互不 import，只通过 `internal/llm` 通信 —— 把「N 个平台 × M 个协议」的复杂度从 N×M 降为 N+M。接入新平台时，三个下游协议一行都不用改。

多平台编排由 [`internal/app/hub.go`](internal/app/hub.go) 的 **Hub** 承担：启动时为每个平台建独立的适配器/号池/登录会话，构建「模型 ID → 平台」索引，请求按模型名路由、未命中透传默认平台。号池（`Pool`）对 Hub 来说就是一个普通 Adapter，多账号与多平台两层互不感知。

接口刻意做小：`Adapter` 只有 3 个方法（`Stream` / `ListModels` / `Name`），`ResponseStream` 只有 1 个（`Recv`）。造假适配器做测试只要十行。

延伸阅读：

- 详细设计 —— [`docs/design/01-架构设计.md`](docs/design/01-架构设计.md)
- 上游协议逆向 —— [`docs/research/02-WorkBuddy上游协议逆向.md`](docs/research/02-WorkBuddy上游协议逆向.md)

---

## 测试

```bash
make check        # gofmt + go vet + go test
make cover        # 覆盖率
go test ./... -race
```

协议转换用**金帧回放**：脱敏后的真实上游样本存在 [`fixtures/`](fixtures/)，测试逐帧喂给解析器，断言产出的 **IR 事件序列**而非字节 —— 完全离线验证，不打真实上游。

当前 12 个包、约 120 个测试用例。核心包覆盖率（`make cover`，数字随代码漂移）：`common` 94%、`obs` 94%、`llm` 84%、`adapter` 64%、`config` 58%、`app` 38%、`workbuddy` 55%。

---

## 常见问题

**启动报「所有平台初始化失败」或 `no_credential`？**
本机没有可用凭证。装过 WorkBuddy / CodeBuddy 桌面客户端且已登录的会自动探测；否则先 `agent2api login` 走设备码登录。

**Claude Code 连上了，但请求总被拦截？**
不要关闭脱敏（去掉 `-no-sanitize`）。客户端固定 system 模板里的安全声明用语会被上游关键词审核误判。

**`model` 可以填什么？**
用 `agent2api models` 或控制台「模型」页查看当前账号实际可用的模型；未知模型名会原样透传给默认平台，由上游决定如何处理。

**非流式请求为什么首字延迟和流式一样？**
上游只支持流式，非流式响应在代理侧聚合而成，无法更早返回。

**怎么改端口 / 监听地址？**
`-port` / `-host`（或对应环境变量，见[配置](#配置)）。要暴露到局域网，必须同时设置 `-api-key`。

**客户端说会话粘性不生效 / 同一会话被分到了不同账号？**
会话粘性按优先级取路由键：`session_id` 等**请求头** → body 里的 `metadata.user_id` / `user`
→ 「系统提示 + 首条用户消息」的哈希。如果你在网关前面放了 Nginx，注意它**默认丢弃带下划线的
请求头**（`session_id` 就在其列），需要在 `http` 或 `server` 块里加
`underscores_in_headers on;`，否则这个信号到不了网关。

**感觉并发一高就触发上游限流？**
默认每账号 4 并发（`upstream.max_concurrency_per_account`），超出部分在网关内排队而不是打上游。
如果上游仍然频繁 429，可以再调低；反之嫌排队太慢（或上游是企业版）、想让请求尽快出去，设 `0` 关闭。
控制台矩阵列头的「在途/上限」读数可以判断是号池被打满还是配置过紧。

---

## 已知限制

**设计限制**

- **上游不支持非流式请求**：非流式在代理侧聚合，首字延迟与流式一致
- **单进程单实例**：冷却状态、会话亲和、并发槽位都在内存，多实例部署时各自独立，会重复冲击上游（共享状态需外部存储，见路线图）
- **并发上限是进程内的**：多实例部署时每个实例各算各的，无法全局限流
- **额度查询未实现**：上游该路由需企业版权限（403）
- **思考过程是单向的**：上游不带思考签名，Anthropic `thinking` block 的 signature 恒为空，多轮会话中上游每次重新推理
- **DSML 文本态工具调用兜底未实现**：实测上游走原生 `tool_calls`，如遇回退需补上

**其他行为**

- **指标持久化默认开启**：缺省写到配置文件同目录的 `metrics.json`（无配置文件时为工作目录的 `agent2api-metrics.json`），含调用统计（模型名、账号、平台、token 数），已在 `.gitignore` 中，但打包发布时需手动排除
- **控制台的「添加账号」需要号池目录**：未配置时凭证写到 `~/.workbuddy`，配好 `-accounts-dir`（或建 `auths/`）后自动入池

---

## 路线图

- [ ] 号池熔断与额度查询
- [ ] 第二个平台适配器（Hub 路由已就绪，差适配器实现）
- [ ] `cmd/probe` 协议漂移检测
- [ ] 多 API Key 分发与每 Key 用量/限额（当前是单密钥 + 进程内全局限流，见 sub2api 的 key 体系）
- [ ] 共享状态（冷却/亲和/并发槽位）以支持多实例部署
- [x] 模型别名路由（`models.aliases`，对应 sub2api 的 composite groups）
- [x] 每账号并发上限与在途可视化（对应 sub2api 的 per-account concurrency limit）
- [x] Prometheus 文本指标（`/metrics`）

---

## 贡献

欢迎 Issue 与 PR，先读 [CONTRIBUTING.md](CONTRIBUTING.md)。

> [!IMPORTANT]
> 提交 Issue 时**请勿粘贴真实凭证、token 或账号昵称**。

---

## 许可证

**尚未确定（TBD）** —— 仓库未附 LICENSE，默认保留所有权利。确定前请勿商用或再分发。

本项目为个人学习与技术研究项目，仅供本人已授权账号在本机或私有环境自用，风险自担。完整条款见 [DISCLAIMER.md](DISCLAIMER.md)。
