# Agent2API

把 AI Agent 平台的私有协议统一翻译成标准 LLM API 的反向代理网关。

当前已实现 **WorkBuddy / CodeBuddy** 平台，对外提供三种标准协议，任何支持 OpenAI / Anthropic 接口的客户端（Claude Code、Codex CLI、各类 SDK 与前端）都能零改造接入。

**简体中文** · [English](README.en.md)

**快速开始** · [它解决什么问题](#它解决什么问题) · [网页控制台](#网页控制台) · [配置](#配置) · [安全须知](#安全须知) · [架构](#架构) · [已知限制](#已知限制) · [贡献](#贡献)

---

> [!WARNING]
> **学习与研究项目，非生产软件。** 仅供**本人已授权账号**在本机或私有环境自用，风险自担。
> 它会读取桌面客户端已登录的**凭证**（凭证 = 账号，切勿外传）；运作方式**可能不符合上游服务条款**。
> 作者不鼓励、也不支持商用或对外提供服务。完整条款见 **[DISCLAIMER.md](DISCLAIMER.md)**。

---

## 它解决什么问题

Claude Code、Codex CLI 这类客户端只认 **OpenAI / Anthropic 标准协议**；而各 AI Agent 平台的私有上游不提供标准接口。

结果是：你手上的平台额度，用不到那些好用的客户端里。

本项目在**本机**做一个协议翻译层 —— 不伪造协议，只是把标准的三种协议翻译成上游私有协议，让你现有的客户端零改造接入。

```
Claude Code / Codex / 任意 OpenAI 客户端
        │  /v1/chat/completions · /v1/responses · /v1/messages
        ▼
   ┌──────────────────────────────────────┐
   │  Agent2API                           │
   │   下游协议层 ── IR 层 ── 上游适配层    │
   └──────────────────────────────────────┘
        │  https://copilot.tencent.com/v2/chat/completions
        ▼
   WorkBuddy 上游
```

### 特性

- **三种协议一次做全**：OpenAI Chat Completions、OpenAI Responses、Anthropic Messages，均支持流式与非流式
- **思考过程**：处理上游的 `delta.reasoning_content`，映射为 `reasoning_content` / `thinking` block / reasoning summary
- **工具调用**：原生 `tool_calls` 通道，含分片参数重组
- **凭证复用**：直接读桌面客户端已登录的凭证，**无需重新登录**
- **内容脱敏**：避免客户端固定模板被上游关键词审核误判（接 Claude Code / Codex 必需）
- **动态模型清单**：运行时从上游拉取，不硬编码
- **内置控制台**：`go:embed` 编进二进制，无前端构建步骤，支持暗色模式

---

## 快速开始

```bash
# 方式一：直接安装（需 Go 1.23+）
go install github.com/576469377/Agent2API/cmd/agent2api@latest

# 方式二：从源码构建
git clone https://github.com/576469377/Agent2API && cd Agent2API
make build                     # 产出 bin/agent2api

# 验证凭证（自动复用桌面客户端已登录的凭证，无需重新登录）
agent2api models               # 或 ./bin/agent2api models

# 启动（默认 127.0.0.1:8787）
agent2api                      # 或 ./bin/agent2api
```

启动后命令行会直接打印控制台地址和全部接口；没装过桌面客户端时用 `agent2api login` 走设备码登录。

> [!WARNING]
> **监听 `0.0.0.0` 且未设 `-api-key`，同网段任何人都能用你的账号额度。** 详见[安全须知](#安全须知)。

### 调用示例

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

### 接入客户端

**Claude Code**：

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
export ANTHROPIC_API_KEY=任意值   # 未配置网关密钥时不会校验
```

**Codex CLI**：把 base_url / API key 指向 `http://127.0.0.1:8787` 与任意值即可（走 `/v1/responses`）。

**任意 OpenAI SDK**：base_url 设为 `http://127.0.0.1:8787/v1`。

> [!TIP]
> 接 Claude Code / Codex 时**不要关闭脱敏**（`-no-sanitize`）：它们的固定 system 模板含大量安全声明用语，会被上游关键词审核误判拦截。

---

## 网页控制台

内置在网关里，无需额外部署，也没有前端构建步骤（静态资源通过 `go:embed` 编进二进制，图表用原生 SVG 绘制）。启动后访问 `http://127.0.0.1:8787`：

| 页面 | 内容 |
|---|---|
| **概览** | 状态墙、请求趋势图、Token 柱状图、调用排行、最近请求；5 秒自动刷新 |
| **对话** | 流式输出、思考过程折叠、采样参数可调、代码块复制、支持表格与公式渲染 |
| **平台** | 上游地址、账号、模型数、健康状态；规划中的平台 |
| **模型** | 模型清单与能力标签，支持筛选 |
| **调用日志** | 最近 200 条请求，可按成功/失败过滤，失败行高亮 |
| **设置** | 脱敏开关（热更新）、重新拉取模型清单、当前生效配置 |

> [!NOTE]
> 核心功能零 CDN 依赖。仅**数学公式渲染**会在首次遇到时按需加载 KaTeX，失败或离线时降级为原始文本，不影响其余功能。控制台截图待补，欢迎贡献（见[贡献](#贡献)）。

### 为多平台预留的结构

- 后端 `adapter` 包定义了**可选**的 `Describer` / `Configurable` 接口，新平台按需实现，不实现也能运行
- `/api/platforms` 返回 `{active, planned}`，新增平台无需改前端
- 指标按**协议**和**模型**两个维度分组，与具体平台无关

---

## 支持的接口

| 接口 | 协议 | 流式 | 非流式 |
|---|:-:|:-:|:-:|
| `POST /v1/chat/completions` | OpenAI Chat Completions | ✅ | ✅ |
| `POST /v1/responses` | OpenAI Responses | ✅ | ✅ |
| `POST /v1/messages` | Anthropic Messages | ✅ | ✅ |
| `GET /v1/models` | OpenAI 模型列表 | — | ✅ |
| `GET /health` | 健康检查 | — | ✅ |
| `GET /` | 网页控制台 | — | ✅ |

能力支持：文本、思考过程、工具调用、多轮对话、系统提示词、采样参数、图片输入。

---

## 配置

优先级：命令行参数 > 环境变量 > 配置文件 > 内置默认。配置文件示例见 [`config.example.json`](config.example.json)。

| 参数 | 环境变量 | 说明 |
|---|---|---|
| `-config` | — | 配置文件路径（JSON） |
| `-port` | `AGENT2API_PORT` | 监听端口，默认 8787 |
| `-host` | `AGENT2API_HOST` | 监听地址，默认 127.0.0.1 |
| `-api-key` | `AGENT2API_API_KEY` | 网关访问密钥，为空则不鉴权 |
| `-credential` | `AGENT2API_CREDENTIAL_PATH` | 凭证文件路径，为空时自动探测 |
| `-base-url` | `AGENT2API_BASE_URL` | 上游地址，默认 [copilot.tencent.com](https://copilot.tencent.com) |
| `-platform` | — | 上游平台，目前仅支持 `workbuddy` |
| `-metrics-file` | — | 指标落盘路径 |
| `-no-persist` | — | 关闭指标落盘（重启后统计清零） |
| `-no-sanitize` | — | 关闭内容脱敏 |

子命令：`agent2api`（启动服务）、`agent2api models`（列出模型）、`agent2api login`（设备码登录）。

---

## 安全须知

本项目会在本机读写**认证凭证**，请务必了解：

1. **凭证来源**：网关不要求输入账号密码，而是读取本机桌面客户端**已登录**的凭证文件（`-credential` 可指定路径）—— 等价于把桌面端的登录态借给本机进程。
2. **凭证文件 = 你的账号**：请勿拷贝到其他机器、勿提交进仓库。`.gitignore` 已默认忽略相关路径。
3. **默认只监听 `127.0.0.1`**：改监听 `0.0.0.0` 且未设 `-api-key` 时，同网段任何人可消耗你的账号额度。共享环境必须设置 `-api-key` 并在前置反向代理做鉴权。
4. **日志与指标**可能记录模型名与 token 数；`-no-persist` 可关闭落盘。

漏洞报告方式见 [SECURITY.md](SECURITY.md)。

---

## 架构

```
internal/
├── llm/          IR 层（零依赖）：请求/响应/错误的中间表示
├── adapter/      上游平台接缝：Adapter 接口
│   └── workbuddy/  WorkBuddy 实现：认证 / 请求 / SSE / 模型 / 脱敏
├── api/          下游协议编解码
│   ├── common/     SSE 写出、错误载荷（三协议共用）
│   ├── openai/chat/
│   ├── openai/responses/
│   └── anthropic/messages/
├── app/          HTTP 路由与编排
├── obs/          指标采集与持久化
├── web/          内置控制台（go:embed）
└── config/       配置
```

**设计要点**：`api/*` 与 `adapter/*` 互不 import，只通过 [`internal/llm`](internal/llm/) 通信。这使得「N 个平台 × M 个协议」的复杂度从 N×M 降为 N+M —— 接入第二个平台时，三个下游协议一行都不用改。

`Adapter` 接口只有 3 个方法（`Stream` / `ListModels` / `Name`），`ResponseStream` 只有 1 个方法（`Recv`），造假适配器做测试的成本极低。

📖 详细设计见 [`docs/design/01-架构设计.md`](docs/design/01-架构设计.md)，上游协议逆向细节见 [`docs/research/02-WorkBuddy上游协议逆向.md`](docs/research/02-WorkBuddy上游协议逆向.md)。

---

## 这个项目适合用来学习什么

按推荐顺序（代码路径均可点击直达）：

| 主题 | 代码位置 |
|---|---|
| **IR 分层架构** —— 零依赖中间表示 + 极小接缝接口，N×M → N+M | [`internal/llm/`](internal/llm/) · [`adapter.go`](internal/adapter/adapter.go) |
| **流式协议转换** —— 三种 SSE 方言与上游私有流的双向映射；Anthropic 块 start/stop 配对是最大的坑 | [`internal/api/`](internal/api/) · [`sse.go`](internal/adapter/workbuddy/sse.go) |
| **金帧回放测试** —— 断言 IR 事件序列而非字节，完全离线验证协议转换 | [`fixtures/`](fixtures/) · [`sse_test.go`](internal/adapter/workbuddy/sse_test.go) |
| **结构化错误分类** —— 错误在产生处一次分类成结构体，全程携带 | [`failure.go`](internal/llm/failure.go) |
| **多层超时控制** —— 空闲看门狗 + 总时长 + 调用方 context；为什么不能用 `http.Client.Timeout` | [`sse.go`](internal/adapter/workbuddy/sse.go) |
| **SSE 批合并写出** —— 一帧多事件一次 Write + 一次 Flush；首字节写出后的错误降级语义 | [`app.go`](internal/app/app.go) |
| **协议逆向方法论** —— 证据等级标注（🟢 实测 / 🟡 推断 / ⚪ 未确认） | [`docs/research/`](docs/research/) |
| **指标与聚合** —— 原子计数器 + 单锁聚合；TPS 分母的取法 | [`metrics.go`](internal/obs/metrics.go) |

**反例也值得看**：[已知限制](#已知限制)如实记录设计取舍，缺陷修复过程（含根因与回归测试）也完整保留在 CHANGELOG 中 —— 「能跑的原型」与「可上生产的系统」之间的真实差距，比只看成功案例更有参考价值。

---

## 与同类项目的关系

协议细节与架构设计参考了以下开源实现，在此致谢：

| 项目 | 语言 | 借鉴之处 |
|---|---|---|
| [hawklithm/workbuddy2api](https://github.com/hawklithm/workbuddy2api) | Python | 协议细节最全：DSML 解析、脱敏词表、请求头清单 |
| [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) | Go | 工程架构：realm 双站、pool / scheduler / session 分层 |
| [WncFht/devin2api](https://github.com/WncFht/devin2api) | Go | **多平台 IR 分层架构的范本**（本项目直接采用其设计思想） |

**本项目的差异**：三个下游协议一次做全（多数实现只有 Chat Completions）；处理了 `delta.reasoning_content`（参考实现普遍遗漏）；模型清单运行时动态拉取（内置清单均已过期）；协议转换有离线金帧回放测试。

---

## 测试

```bash
make check        # go vet + go test
make cover        # 覆盖率
go test ./... -race
```

协议转换使用**金帧回放**测试：脱敏后的真实上游样本存放在 [`fixtures/`](fixtures/)，逐帧喂给解析器，断言产出的 **IR 事件序列**而非字节 —— 协议转换完全离线验证，不打真实上游。

覆盖率现状（实测，如实列出）：

| 包 | 覆盖率 |
|---|---|
| [`internal/api/common`](internal/api/common/) | 94.3% |
| [`internal/obs`](internal/obs/) | 93.3% |
| [`internal/llm`](internal/llm/) | 88.6% |
| [`internal/config`](internal/config/) | 75.0% |
| [`internal/api/anthropic/messages`](internal/api/anthropic/messages/) | 56.4% |
| [`internal/api/openai/responses`](internal/api/openai/responses/) | 55.9% |
| [`internal/adapter/workbuddy`](internal/adapter/workbuddy/) | 55.2% |
| [`internal/api/openai/chat`](internal/api/openai/chat/) | 53.1% |
| [`internal/app`](internal/app/) | 50.1% |
| [`cmd/agent2api`](cmd/agent2api/) · [`internal/web`](internal/web/) | 0.0%（CLI 薄壳 / go:embed 静态资源，无可测逻辑） |

---

## 已知限制

### 设计限制

- **上游不支持非流式请求**：非流式响应在代理侧聚合，首字延迟与流式一致
- **多账号**：已支持同平台多账号轮询与限流冷却（见[多账号号池](#多账号号池)）；尚未实现熔断与额度查询
- **额度查询端点未实现**：上游该路由需企业版权限，返回 403
- **DSML 文本态工具调用兜底未实现**：实测上游走原生 `tool_calls` 通道，如遇回退需补上

### 多账号号池

一个账号额度不够用？把多个凭证放进号池目录，网关自动轮询、限流自动换号：

```bash
# 1. 逐个登录，把凭证存进 auths/（目录名可自定义，配合 -accounts-dir 或 config 的 accounts_dir）
agent2api login -out auths/account-a.json
agent2api login -out auths/account-b.json

# 2. 启动网关（工作目录存在 auths/ 时会自动启用，也可显式指定）
agent2api                          # 自动扫描 auths/
agent2api -accounts-dir auths      # 显式指定
```

行为：

- **轮询**：请求在健康账号间均匀分发
- **限流冷却**：某账号触发 429 时，解析上游文案里的精确重置时刻（如「将在 2026-09-16 23:21:31 UTC+8 重置」）做**到点解冻**，期间请求自动落到其余账号；解析失败退化为 60s 冷却
- **鉴权失败冷却**：刷新后仍 401 的账号冷却 10 分钟（多半是账号失效/权益异常）
- **参数错误不换号**：请求本身有问题（上下文超限等）时直接失败——换任何账号结果都一样
- **启动横幅**逐账号显示状态（✓ 健康 / ⏳ 冷却中）；桌面客户端凭证与号池可并存（显式 credential 优先）
- 桌面客户端**下线不影响**网关运行：凭证只是启动时读一次，后续刷新由网关自己完成

### 已知缺陷（7 项已全部修复，2026-09-16）

以下问题曾长期存在，现已在对应位置修复并有回归测试锁定（见 `CHANGELOG.md` 与各测试文件）：

1. ~~Chat Completions 流式中断静默截断~~ —— `EventError` 现发出 `{"error":{...}}` 帧（官方 SDK 契约），不再谎报正常收尾。回归：`chat_test.go` / `app_test.go`。
2. ~~Anthropic 流式 `input_tokens` 恒为 0~~ —— `message_delta` 补发真实值（官方 SDK 累积覆盖语义）。回归：`messages_test.go`。
3. ~~Responses 协议 `output` 可能丢项~~ —— 按 map 实际 key 遍历，未完成块合成 `incomplete` 终态。回归：`responses_test.go`。
4. ~~工具描述未脱敏~~ —— `convertTools` 已接线 `SanitizeToolDescription`。回归：`payload_test.go`。
5. ~~没有任何重试 / 退避~~ —— dial 阶段最多 3 次、指数退避 + Retry-After；401 刷新每轮至多一次（防死循环）；401/429 分类显式置位。回归：`adapter_test.go`。
6. ~~控制台无法携带 API Key~~ —— 前端全链路携带 `X-Api-Key`，401 引导输入并验证后持久化；新增不鉴权的 `/api/auth-hint`；鉴权失败返回正确的 401（原 400）。
7. ~~测试缺口~~ —— `llm` 88.6%、`config` 75%、`chat` 53.1%、`app` 50.1%、`common` 94.3%（原 0%）。

### 其他已知行为

- **指标持久化默认开启**：无配置文件时写到当前工作目录的 `agent2api-metrics.json`（含调用统计），已在 `.gitignore` 中，但**打包发布时必须手动排除**。

---

## 路线图

- [x] 修复 7 项已知缺陷（2026-09-16，见上方「已知缺陷」）
- [x] 接入重试与退避（dial 退避 + Retry-After + 401 刷新闸门）
- [x] 多账号号池：轮询 + 限流到点冷却 + 401 冷却（2026-09-16，见「多账号号池」）
- [ ] 号池熔断与额度查询
- [ ] 第二个平台适配器（Devin / Cursor）
- [ ] `cmd/probe` 协议漂移检测

---

## 贡献

欢迎提 Issue 与 PR，请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。

> [!IMPORTANT]
> 提交 Issue 时**请勿粘贴真实凭证、token 或账号昵称**。

---

## 许可证

**尚未确定（TBD）**。仓库未附带 LICENSE 文件，默认保留所有权利 —— 确定前请勿商用或再分发。确定后会同步更新本文件与 [README.en.md](README.en.md)。

---

## 合规与免责

本项目为**个人学习与技术研究项目**，仅供本人已授权账号在本机或私有环境自用，风险自担。作者不鼓励、也不支持商用或对外提供服务。

完整条款见 **[DISCLAIMER.md](DISCLAIMER.md)** · 相关文档：[SECURITY.md](SECURITY.md) · [CONTRIBUTING.md](CONTRIBUTING.md) · [CHANGELOG.md](CHANGELOG.md)
