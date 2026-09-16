# Agent2API

把 AI Agent 平台的私有协议统一翻译成标准 LLM API 的反向代理网关。

当前已实现 **WorkBuddy / CodeBuddy** 平台，对外提供三种标准协议，任何支持 OpenAI / Anthropic 接口的客户端（Claude Code、Codex CLI、各类 SDK 与前端）都能零改造接入。

**简体中文** · [English](README.en.md)

---

> ## ⚠️ 使用前必读
>
> 本项目仅供**本人已授权账号**在**本机或私有环境**中自用，**风险自担**。
>
> - 它读取你本机桌面客户端已登录的**凭证**（凭证 = 你的账号，切勿外传）
> - 它的运作方式**可能不符合**上游平台的服务条款
> - 上游为私有协议，**可能随时变更**导致失效
> - 已知缺陷清单**并不完整**，见下方[已知限制](#已知限制)
>
> 完整条款见 **[DISCLAIMER.md](DISCLAIMER.md)**。

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
- **内置控制台**：`go:embed` 编进二进制，无前端构建步骤

---

## 与同类项目的关系

本项目的协议细节与架构设计参考了以下开源实现，在此致谢：

| 项目 | 语言 | 借鉴之处 |
|---|---|---|
| [hawklithm/workbuddy2api](https://github.com/hawklithm/workbuddy2api) | Python | 协议细节最全：DSML 解析、脱敏词表、请求头清单 |
| [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) | Go | 工程架构：realm 双站、pool / scheduler / session 分层 |
| [WncFht/devin2api](https://github.com/WncFht/devin2api) | Go | **多平台 IR 分层架构的范本**（本项目直接采用其设计思想） |

**本项目的差异**：

- 三个下游协议一次做全（多数实现只有 Chat Completions）
- 处理了 `delta.reasoning_content`（参考实现普遍遗漏，导致思考过程被静默丢弃）
- 模型清单**运行时动态拉取**（参考实现内置的清单均已过期）
- 协议转换有**离线金帧回放测试**，不打真实上游

---

## 快速开始

```bash
# 1. 获取源码
git clone <你的仓库地址> && cd Agent2API

# 2. 编译到 bin/agent2api
make build

# 3. 验证凭证（自动复用桌面客户端已登录的凭证，无需重新登录）
./bin/agent2api models

# 4. 启动（默认 127.0.0.1:8787）
./bin/agent2api
```

> **注意**：`go.mod` 的 module path 是 `agent2api`，不是可解析的仓库路径，因此**不支持 `go install`**，请用 `make build` 或 `go build -o bin/agent2api ./cmd/agent2api`。
> 若你 fork 到自己的仓库，建议改成自己的路径，见[下方说明](#fork-后建议修改-module-path)。

启动后命令行会直接打印控制台地址和全部接口：

```
──────────────────────────────────────────────────────────────
  Agent2API 控制台    http://127.0.0.1:8787/

  workbuddy · 账号 <本机登录账号> · <N> 个模型
──────────────────────────────────────────────────────────────
  接口
    POST  http://127.0.0.1:8787/v1/chat/completions   OpenAI Chat Completions
    POST  http://127.0.0.1:8787/v1/responses          OpenAI Responses
    POST  http://127.0.0.1:8787/v1/messages           Anthropic Messages
    GET   http://127.0.0.1:8787/v1/models             模型清单
    GET   http://127.0.0.1:8787/health                健康检查
──────────────────────────────────────────────────────────────
  提示  未配置 API Key，网关对本机可访问者开放（启动时加 -api-key 设置）
        Ctrl+C 停止服务
```

> 监听 `0.0.0.0` 时横幅会显示 `localhost`，那才是实际能打开的地址。
> **监听 `0.0.0.0` 且未设 `-api-key`，同网段任何人都能用你的账号额度**（见[安全须知](#安全须知)）。

> 如果你的机器上没装过 WorkBuddy 桌面客户端，或想用另一个账号：
> ```bash
> ./bin/agent2api login      # 设备码登录，按提示在浏览器完成授权
> ```

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

> 接 Claude Code / Codex 时**不要关闭脱敏**（`-no-sanitize`），它们的固定 system 模板含大量安全声明用语，会被上游误判拦截。

---

## 网页控制台

内置在网关里，无需额外部署，也没有前端构建步骤（静态资源通过 `go:embed` 编进二进制，图表用原生 SVG 绘制）。

启动后访问 `http://127.0.0.1:8787`：

| 页面 | 内容 |
|---|---|
| **概览** | 状态墙（请求总数 / 成功率 / 平均延迟 / 并发 / Token）、请求趋势图、Token 柱状图、协议与模型调用排行、最近请求。5 秒自动刷新 |
| **对话** | 流式逐字输出、思考过程折叠、模型与采样参数可调、代码块一键复制、停止生成 |
| **平台** | 已接入平台的上游地址、账号、模型数、健康状态；并列出规划中的平台 |
| **模型** | 模型清单与能力标签（工具 / 思考 / 视觉 / 默认），支持筛选 |
| **调用日志** | 最近 200 条请求，含状态、协议、模型、耗时、Token、错误信息，可按成功/失败过滤 |
| **设置** | 脱敏开关（热更新）、重新拉取模型清单、当前生效配置、接入方式 |

> **CDN 依赖说明**：核心功能零 CDN 依赖。仅**数学公式渲染**（`$...$`）会在首次遇到时从 jsDelivr 按需加载 KaTeX 0.16.11（CSS + JS）；加载失败或离线时自动降级为原始文本显示，**不影响其余功能**。

> **截图**：仓库目前没有控制台截图，欢迎贡献（见 [CONTRIBUTING.md](CONTRIBUTING.md)）。在此之前请直接启动查看。

### 为多平台预留的结构

控制台不写死任何平台：

- 后端 `adapter` 包定义了**可选**的 `Describer` / `Configurable` 接口，新平台按需实现，不实现也能正常运行
- `/api/platforms` 返回 `{active: [...], planned: [...]}`，新增平台只需在适配器注册表里加一项，前端无需改动
- 指标按**协议**和**模型**两个维度分组统计，与具体平台无关

新增一个平台的成本：实现 `adapter.Adapter` + 可选 `Describer`，再在 `cmd` 里注册。三个下游协议一行都不用改。

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

能力支持：文本、思考过程（`reasoning_content` / `thinking` / reasoning summary）、工具调用、多轮对话、系统提示词、采样参数、图片输入。

---

## 配置

优先级：命令行参数 > 环境变量 > 配置文件 > 内置默认。

配置文件（`config.json`，通过 `-config` 指定）示例见 `config.example.json`。

| 参数 | 环境变量 | 说明 |
|---|---|---|
| `-config` | — | 配置文件路径（JSON） |
| `-port` | `AGENT2API_PORT` | 监听端口，默认 8787 |
| `-host` | `AGENT2API_HOST` | 监听地址，默认 127.0.0.1 |
| `-api-key` | `AGENT2API_API_KEY` | 网关访问密钥，为空则不鉴权 |
| `-credential` | `AGENT2API_CREDENTIAL_PATH` | 凭证文件路径，为空时自动探测 |
| `-base-url` | `AGENT2API_BASE_URL` | 上游地址，默认 `https://copilot.tencent.com` |
| `-platform` | — | 上游平台，目前仅支持 `workbuddy` |
| `-metrics-file` | — | 指标落盘路径；默认写到配置文件同目录的 `metrics.json` |
| `-no-persist` | — | 关闭指标落盘（重启后统计清零） |
| `-no-sanitize` | — | 关闭内容脱敏 |

子命令：`agent2api`（启动服务）、`agent2api models`（列出模型）、`agent2api login`（设备码登录）。

> **关于脱敏**：上游有关键词级内容审核，Claude Code / Codex 的固定 system 模板含大量合规声明词（DoS、exploit、credential testing 等），会被误判拦截。默认开启脱敏；**接入 Claude Code / Codex 时不建议关闭**。

---

## 安全须知

本项目会在本机读写**认证凭证**，请务必了解以下几点：

1. **凭证来源**：网关**不要求你输入账号密码**，而是读取本机 WorkBuddy 桌面客户端**已登录**的凭证文件（`-credential` 可指定路径）。等价于把桌面端的登录态借给本机进程使用。
2. **凭证文件 = 你的账号**：请勿拷贝到其他机器、勿提交进仓库。仓库的 `.gitignore` 已默认忽略相关路径（`config.json`、`*.session.json`、`auths/`、`*.info`）。
3. **默认只监听 `127.0.0.1`**：⚠️ 若你改为监听 `0.0.0.0` 且**未设置 `-api-key`**，同网段的任何人都可以调用你的账号额度。共享或公网环境**必须**同时设置 `-api-key` 并在前置反向代理上做鉴权。
4. **控制台不发 API Key**（已知缺陷）：网页控制台不会携带 `-api-key` 请求 `/api/*`，因此**设置 `-api-key` 后，控制台的概览/平台/模型/调用日志会返回 401**。网关 API 本身的鉴权是正常的。详见[已知限制](#已知限制)。
5. **日志与指标**：可能记录模型名与 token 数；`-no-persist` 可关闭指标落盘。

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

**设计要点**：`api/*` 与 `adapter/*` 互不 import，只通过 `llm` 通信。这使得「N 个平台 × M 个协议」的复杂度从 N×M 降为 N+M —— 后续接入第二个平台时，三个下游协议一行都不用改。

`Adapter` 接口只有 3 个方法（`Stream` / `ListModels` / `Name`），`ResponseStream` 只有 1 个方法（`Recv`），因此造假适配器做测试的成本极低。

详细设计见 [`docs/design/01-架构设计.md`](docs/design/01-架构设计.md)，上游协议逆向细节见 [`docs/research/02-WorkBuddy上游协议逆向.md`](docs/research/02-WorkBuddy上游协议逆向.md)。

---

## 测试

```bash
make check        # go vet + go test
make cover        # 覆盖率
go test ./... -race
```

协议转换使用**金帧回放**测试：脱敏后的真实上游响应样本存放在 [`fixtures/`](fixtures/)，测试时逐帧喂给解析器，断言产出的 IR 事件序列而非字节。这样协议转换可以完全离线验证，不打真实上游。

**覆盖率现状**（`go test ./... -cover` 实测，如实列出）：

| 包 | 覆盖率 |
|---|---|
| `internal/obs` | 93.4% |
| `internal/api/anthropic/messages` | 56.0% |
| `internal/api/openai/responses` | 49.2% |
| `internal/adapter/workbuddy` | 27.1% |
| `cmd/agent2api`、`internal/api/common`、`internal/api/openai/chat`、`internal/app`、`internal/config`、`internal/llm`、`internal/web` | **0.0%** |

编排核心（`internal/app`）与 Chat 编解码器（`internal/api/openai/chat`）目前**没有测试**。欢迎补充，见 [CONTRIBUTING.md](CONTRIBUTING.md)。

---

## 已知限制

### 设计限制

- **上游不支持非流式请求**：非流式响应在代理侧聚合，首字延迟与流式一致。
- **单账号**：未实现多账号池与熔断。
- **额度/积分查询端点未实现**：上游该路由需要企业版权限，返回 403。
- **DSML 文本态工具调用兜底通道未实现**：实测上游目前走原生 `tool_calls` 通道；如遇回退需补上。

### 已知缺陷（尚未修复）

以下为经代码审查与实测**确认存在**的问题，按影响排序：

1. **Chat Completions 流式中断会静默截断**（影响最大）
   `internal/api/openai/chat/chat.go` 中对 `EventError` 的编码返回 **0 个帧**。上游中途失败时，客户端已收到 HTTP 200，随后内容直接截断 —— **既没有 `finish_reason`，也没有 `[DONE]`，更没有错误帧**。客户端无法区分「正常结束」与「上游挂了」。
2. **Anthropic 流式 `input_tokens` 恒为 0**
   `internal/api/anthropic/messages/messages.go` 的 `message_start` 硬编码 `"input_tokens": 0`，流式模式下从不报告输入 token。依赖 token 计费的客户端会看到 0。
3. **Responses 协议的 `output` 可能丢项**
   `internal/api/openai/responses/responses.go` 用 `for i := 0; i < len(e.itemID); i++` 遍历一个按内容下标索引的稀疏 map。若某块「已 start 但未 end」（例如流在工具调用中途断开），该条目会从 `output[]` 中静默消失。
4. **工具描述未脱敏**
   `internal/adapter/workbuddy/sanitize.go` 中的 `SanitizeToolDescription` 已实现但**从未被调用** —— 只有 system 提示词走了脱敏。工具描述若含敏感词，仍可能被上游拦截。
5. **没有任何重试 / 退避**
   全链路只有「401 时重新取一次凭证再拨号」。上游 5xx / 429 会直接失败给客户端。（相关函数 `isRetryableTransportError`、`parseRetryAfter`、`Failure.RetryAfterSeconds` 目前是**死代码**。）
6. **控制台无法携带 API Key**
   前端不发鉴权头，因此设置 `-api-key` 后控制台的 `/api/*` 调用全部 401。见[安全须知](#安全须知)第 4 条。
7. **测试缺口**
   编排核心、Chat 编解码器、配置层均无测试；12 个包中 7 个覆盖率为 0.0%。

### 其他已知行为

- **指标持久化默认开启**：无配置文件时会写到**当前工作目录**的 `agent2api-metrics.json`（含你的调用统计）。该文件已在 `.gitignore` 中，但**打包发布（zip/tar）时必须手动排除**。
- **`go install` 不可用**：见[快速开始](#快速开始)的说明。

---

## 路线图

- [ ] 补 `internal/app`、`internal/api/openai/chat`、`internal/config` 的测试
- [ ] 修复上述 7 项已知缺陷
- [ ] 接入重试与退避（接线已有的 `isRetryableTransportError` / `parseRetryAfter`）
- [ ] 第二个平台适配器（Devin / Cursor）
- [ ] `cmd/probe` 协议漂移检测
- [ ] 多账号池 + 冷却 + 熔断

---

## 贡献

欢迎提 Issue 与 PR，请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。

> ⚠️ 提交 Issue 时**请勿粘贴真实凭证、token 或账号昵称**。

---

## 许可证

**尚未确定（TBD）**。

当前仓库**未附带 LICENSE 文件**，因此默认保留所有权利 —— 在许可证确定前，请勿用于商业用途或再分发。

确定后会同步更新本文件与 [README.en.md](README.en.md)。

---

## Fork 后建议修改 module path

`go.mod` 当前为 `module agent2api`（不是可解析的导入路径）。若你 fork 到自己的仓库，建议改成真实路径以启用 `go install` 与 pkg.go.dev 文档：

```bash
# 1. 批量替换 import（约 20 个文件 / 38 处）
grep -rl '"agent2api/' --include='*.go' . \
  | xargs perl -pi -e 's{"agent2api/}{"github.com/<你的用户名>/Agent2API/}g'

# 2. 修改 go.mod 第一行
sed -i '' '1s|.*|module github.com/<你的用户名>/Agent2API|' go.mod

# 3. 验证
go mod tidy && go build ./... && go test ./...
```

---

## 合规与免责

本项目仅供**本人已授权账号**在**本机或私有环境**中自用，**风险自担**。使用前请阅读 **[DISCLAIMER.md](DISCLAIMER.md)**。

相关文档：[SECURITY.md](SECURITY.md) · [CONTRIBUTING.md](CONTRIBUTING.md) · [CHANGELOG.md](CHANGELOG.md)
