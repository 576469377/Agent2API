# Agent2API

**把 AI Agent 平台的私有协议，统一转换为标准 LLM API 的多平台本地网关。**

通过平台适配器接入不同上游，对外提供 Chat Completions、Responses 和 Anthropic Messages，共用模型路由、账号调度、流式转换与管理控制台。**目前已接入 WorkBuddy / CodeBuddy 国内版与 WorkBuddy 国际版**，后续平台可沿用同一套协议与管理能力。

[![CI](https://github.com/576469377/Agent2API/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/576469377/Agent2API/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.23%2B-00ADD8?logo=go&logoColor=white)](go.mod)

**简体中文** · [English](README.en.md) · [文档导航](docs/README.md) · [更新日志](CHANGELOG.md) · [反馈问题](https://github.com/576469377/Agent2API/issues)

![Agent2API：可扩展的平台适配器、统一网关与标准 API](docs/assets/gateway.svg)

> [!IMPORTANT]
> 个人学习与研究项目，仅限本人已授权账号在本机或受控私有环境使用。上游是私有协议，可能随时变化；使用方式可能与上游条款冲突。请阅读[免责声明](DISCLAIMER.md)，勿公开提供服务或提交凭证。仓库尚未选定许可证。

## 它解决什么问题

让 Claude Code、ZCode、Codex CLI 及其他兼容标准 API 的客户端连接同一个本地入口。上游接入由适配器完成，客户端继续使用标准协议；新增平台时复用现有协议层、路由和控制台。

| 能力 | 当前实现 |
|---|---|
| 已接入平台 | WorkBuddy / CodeBuddy 国内版、WorkBuddy 国际版 |
| 扩展方式 | 实现平台适配器并注册到 Hub；其他平台尚未实现 |
| 协议 | Chat Completions、Responses、Anthropic Messages；流式与非流式 |
| 路由 | 平台前缀、模型别名、可选的建流前同名回退 |
| 账号 | 区域隔离、凭证自动发现与刷新、多账号调度、会话亲和、并发上限 |
| 对话 | 文本、图片输入、思考输出、原生工具调用；具体能力以模型目录为准 |
| 管理 | 九页控制台、交互趋势与用量分析、组合日志筛选、账号表格、客户端接入、中英文与深浅主题 |

## 快速开始

需要 **Go 1.23+**。以下以目前支持的 WorkBuddy 为例，可复用桌面客户端登录态，也可先启动网关再从控制台登录。从默认分支构建当前代码；完整安装、设备登录与验证步骤见[快速开始](docs/quickstart.md)。

```powershell
# Windows PowerShell
git clone https://github.com/576469377/Agent2API.git
cd Agent2API
go build -trimpath -o bin/agent2api.exe ./cmd/agent2api
./bin/agent2api.exe
```

```bash
# macOS / Linux
git clone https://github.com/576469377/Agent2API.git
cd Agent2API
go build -trimpath -o bin/agent2api ./cmd/agent2api
./bin/agent2api
```

启动后打开 **[http://127.0.0.1:8787](http://127.0.0.1:8787)**，在「平台连接」确认区域，在「模型目录」复制完整模型 ID，再到「模型测试」验证一次回复。

- 默认保留国内版与国际版；没有凭证的平台显示「待登录」，可以在控制台添加首个账号。
- 只启用国内版：启动参数 `-platform workbuddy`；国际版：`-platform workbuddy-global`。
- 没有桌面登录态：在「账号管理 → 添加账号」选择国内版或国际版；也可执行 `login -region cn/global`。
- 国际桌面加密凭证已在 Windows 验证；macOS / Linux 尚未完成该凭证路径的端到端验证，见[国际版说明](docs/research/04-WorkBuddy国际版接入.md)。

## 接入客户端

客户端连接网关端口 **8787**。独立控制台默认使用的 **8788** 是管理入口。

| 接入方式 | 地址 / 路径 | 模型填写 |
|---|---|---|
| Chat Completions 客户端 | Base URL：`http://127.0.0.1:8787/v1` | 从目录复制模型 ID |
| Responses 客户端 | `POST http://127.0.0.1:8787/v1/responses` | 从目录复制模型 ID |
| Claude Code | `ANTHROPIC_BASE_URL=http://127.0.0.1:8787` | 可配置模型别名 |
| ZCode | Anthropic Messages；Base URL：`http://127.0.0.1:8787/v1` | 推荐 `平台/模型` 前缀 |

未设置网关密钥时不校验 API Key，客户端要求必填可使用 `agent2api-local`。设置密钥后，各客户端必须填写同一个网关密钥。接入客户端时保留默认的内容脱敏设置。

ZCode 可用仓库脚本增量同步两个区域的模型，保留已有排序、启用状态、能力与默认选择。先启动网关，并准备本地配置：

```powershell
Copy-Item config.example.json config.json  # 仅首次创建；已有配置不要覆盖
python scripts/sync-zcode.py --config config.json --dry-run
python scripts/sync-zcode.py --config config.json
```

详见[客户端接入](docs/clients.md)，包含鉴权、协议示例、ZCode 备份与恢复。

## 模型与路由

| 请求中的模型 ID | 行为 |
|---|---|
| `workbuddy/模型名` | 固定国内版 |
| `workbuddy-global/模型名` | 固定国际版 |
| 裸模型名 | 按平台配置顺序选择，同名时第一个平台优先 |
| 配置中的别名 | 映射到真实模型，可固定平台 |

`models.fallback_on_unavailable` 默认关闭。开启后，裸名在建流前遇到额度不足、限流或 HTTP 5xx，可尝试其他平台的同名模型。显式平台前缀和固定平台的别名不跨区域；流开始后不回退。

模型目录按账号动态获取。**目录支持、账号可调度和剩余额度是不同信息**；目录中的条目不保证当前可以生成。详见[配置与模型路由](docs/configuration.md)。

## 网页控制台

概览、调用日志、用量分析、账号管理、平台连接、模型目录、模型测试、客户端接入和设置统一管理已接入平台，目前包含 WorkBuddy 国内版与国际版。交互趋势、运行诊断与累计排行可联动查看调用日志；账号支持表格和卡片，接入页提供三种协议的真实网关地址与请求示例。

界面随网关嵌入，无前端构建步骤。已有网关正在使用时，独立控制台可从磁盘加载新版界面，**无需重启原反代或修改客户端地址**。见[控制台使用说明](docs/console.md)。常规页面使用本地资源；数学公式按需加载 KaTeX，离线时显示原文。

## 文档

| 想做什么 | 阅读 |
|---|---|
| 安装、登录、启动并验证 | [快速开始](docs/quickstart.md) |
| 配置 ZCode 或其他客户端 | [客户端接入](docs/clients.md) |
| 配置区域、模型前缀、别名与回退 | [配置与模型路由](docs/configuration.md) |
| 管理账号、冷却与并发 | [账号与调度](docs/accounts.md) |
| 使用或更新控制台 | [控制台](docs/console.md) |
| 查看接口、鉴权和请求示例 | [API 参考](docs/api.md) |
| 排查密钥、模型、额度与登录问题 | [故障排查](docs/troubleshooting.md) |
| 理解代码、参与维护 | [当前架构](docs/architecture.md) · [贡献指南](CONTRIBUTING.md) |

历史设计、协议调研与实测报告见[文档导航](docs/README.md)。

## 配置

完整 CLI / 环境变量 / JSON 对照、双平台示例、别名、并发与 Prometheus 配置已移至[配置与模型路由](docs/configuration.md)。本地配置从 [config.example.json](config.example.json) 开始。

## 多账号号池

本人已授权的多个账号可以按健康度调度，支持会话亲和、按错误类型换号、并发上限和按模型冷却。两个区域的凭证目录应分开，详见[账号与调度](docs/accounts.md)。

## 支持的接口

三种生成协议分别位于 `/v1/chat/completions`、`/v1/responses`、`/v1/messages`，另提供 `/v1/models`、`/health` 与 `/metrics`。请求与鉴权示例见[API 参考](docs/api.md)。

## 安全须知

凭证等同账号访问权限。不要上传 `config.json`、账号文件、ZCode 配置或运行指标；反馈时隐藏密钥、昵称和上游请求 ID。默认仅监听 `127.0.0.1`；若修改监听范围，必须配置访问密钥及相应访问控制。详见[安全策略](SECURITY.md)。

## 架构

协议编解码通过零依赖的 IR 与上游适配器通信；Hub 负责多平台路由，各平台使用独立号池。详见[当前实现架构](docs/architecture.md)，历史设计稿另存于文档导航。

## 已知限制

- 上游私有接口没有稳定性承诺，模型可用范围及额度由上游和账号决定。
- 非流式回复由上游流聚合后返回；网关不实现完整 OpenAI / Anthropic 产品 API。
- 未实现余额查询；账号显示「可调度」不等于仍有额度。
- 账号冷却、会话亲和及并发槽位属于单进程，多实例不会共享这些状态。
- 思考输出没有上游签名；文本态 DSML 工具调用兜底尚未实现。
- 国际桌面加密凭证目前仅在 Windows 完成端到端验证。

## 测试

```bash
go test ./...
go vet ./...
node --test scripts/test-console-core.mjs scripts/test-console-monitor.mjs
python -m unittest discover -s scripts -p test_sync_zcode.py
```

测试使用离线样本与模拟服务，不消耗真实模型额度。前端测试需要 Node.js，ZCode 脚本及测试需要 Python 3.9+；网关本身不要求这两项运行时。竞态检查与开发流程见[贡献指南](CONTRIBUTING.md)。

## 常见问题

密钥无效、模型重名、额度不足、国际登录失败和 ZCode 配置加载问题，见[故障排查](docs/troubleshooting.md)。

## 路线图

- [x] 国内版与国际版统一管理、平台限定名与同名回退
- [x] 控制台重构与不重启网关的独立入口
- [x] ZCode 模型增量同步与设置保留
- [x] 多账号调度、并发上限、模型别名、Prometheus 指标
- [ ] 国际桌面加密凭证的 macOS / Linux 实测
- [ ] 更多上游平台与协议漂移检测
- [ ] 额度查询、多密钥用量管理及共享调度状态

## 贡献

欢迎 Issue 与 PR，请先读[贡献指南](CONTRIBUTING.md)。漏洞请按[安全策略](SECURITY.md)私下报告，公开 Issue 不要附真实凭证。

## 许可证

**尚未确定（TBD）** —— 仓库未附 LICENSE，默认保留所有权利。确定前请勿商用或再分发。

本项目为个人学习与技术研究项目，仅供本人已授权账号在本机或私有环境自用，风险自担。完整条款见[免责声明](DISCLAIMER.md)。
