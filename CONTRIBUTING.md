# 贡献指南

感谢有兴趣参与本项目。请先阅读 [DISCLAIMER.md](DISCLAIMER.md) 与 README 的[已知限制](README.md#已知限制)章节。

## 环境要求

- **Go 1.23+**（`go.mod` 声明 `go 1.23.0`）
- 无需任何前端工具链（静态资源通过 `go:embed` 编进二进制）
- 运行依赖包括 `golang.org/x/text`（GBK 解码）、`modernc.org/sqlite`（cc-switch 原生事务与在线备份，无需 CGO）和 `go-toml/v2`（保留 Codex TOML 配置字段）
- 前端离线测试需要支持 `node --test` 的 Node.js；ZCode、诊断脚本与测试需要 Python 3.9+。普通网关构建不需要这两项；Cursor 在 macOS / Linux 读取登录态时需要 Python 3。
- 提交凭证检查同样需要 Python 3.9+。安装 `scripts/pre-commit` 后会检查暂存内容；CI 还会扫描可达历史，诊断只显示文件、位置和类型，不打印密钥值。

使用指南见[文档导航](docs/README.md)，阅读源码先看[开发与仓库导览](docs/development.md)。修改配置、模型路由或客户端行为时，请同步中英文首页及对应指南；指标变更还需更新[口径定义](docs/metrics.md)。

## 构建与测试

```bash
make build      # 编译到 bin/agent2api
make run        # 编译并运行
make test       # go test ./cmd/... ./internal/...
make vet        # go vet ./cmd/... ./internal/...
make fmt        # gofmt -w cmd internal
make check      # fmt + vet + test（提交前请跑这个）
make cover      # 覆盖率
make test-race  # 竞态检测（CI 也跑）
```

Windows 无需安装 Make，直接使用以下跨平台命令：

```powershell
go build -trimpath -o bin/agent2api-dev.exe ./cmd/agent2api
go test ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
node --test scripts/test-console-*.mjs
python -m unittest discover -s scripts -p 'test_*.py'
python scripts/check-docs.py
python scripts/check-secrets.py --staged
python -m unittest discover -s scripts -p test_check_secrets.py
```

已有网关在使用时，不覆盖或启动当前服务的可执行文件。前端开发使用[独立控制台](docs/console.md)，与原网关共存；后端测试使用离线样本或独立端口。`make run` 会启动网关；`make clean` 只删除 Make 构建的程序和覆盖率文件，保留 `bin/` 内其他候选程序、诊断和研究资料。

本地测试与格式化限定在 `cmd/`、`internal/`，避免扫描 `bin/` 中临时研究程序。干净检出的 CI 使用 `go test ./...`：Linux / macOS 同时验证 Go 1.23 和 stable 并执行竞态检测，Windows stable 验证文件替换、SQLite 和进程行为。`make lint` 会将已安装检查器的失败状态传给调用方。

`check-docs.py` 离线检查根目录、`docs/`、`fixtures/` 和 `.github/` 的 Markdown 本地目标、页内标题与代码块；CI 同时验证检查器。它不验证外部站点在线状态，也不能替代命令、默认值和能力说明与代码的交叉核对。当前用户指南与带日期的历史研究分开维护，保留已有文档路径，避免破坏引用。

## 提交前的自查清单

- [ ] `make check` 通过
- [ ] 涉及协议转换的改动**必须**补/更新测试
- [ ] `gofmt` 已格式化（`make fmt`）
- [ ] **没有提交任何凭证**（见下方「切勿提交」）
- [ ] 文档检查通过，当前指南与历史计划的状态明确

## 测试要求

### 协议转换必须用金帧回放

新增或修改协议转换逻辑时，请用 `fixtures/` 里的**真实上游样本**做离线断言：

1. 样本存放在 [`fixtures/`](fixtures/)，测试时逐帧喂给解码器
2. **断言产出的 IR 事件序列，而不是字节** —— 这样测试与协议细节解耦
3. 参考实现：[`internal/adapter/workbuddy/sse_test.go`](internal/adapter/workbuddy/sse_test.go) 的 `replayFixture`

好处：协议转换可以完全离线验证，不需要真实上游账号，也不会因为上游抖动而 flaky。

> ⚠️ **不要用未经脱敏的真实抓包覆盖 `fixtures/` 中的文件** —— 会重新引入个人数据（上游请求 ID、抓包时间戳）。详见 [`fixtures/README.md`](fixtures/README.md)。

### 编解码器测试

三种下游协议的编解码器测试在各自包内（`messages_test.go`、`responses_test.go`），做法是用 `fakeStream` 构造 IR 事件序列，断言产出的 SSE 帧。

### 当前测试缺口

`internal/app`、`internal/api/openai/chat`、`internal/config` 此前长期无测试，**现已补齐**（`app_test.go` / `matrix_test.go`、`chat_test.go`、`config_test.go`）。

可优先补充以下覆盖；覆盖率会随代码变化，请先运行 `make cover` 获取当前结果：

- [`internal/web`](internal/web/) —— 已覆盖静态资源的内容 ETag、304、HEAD、Range 与升级失效；前端流式处理与监控统计分别有 `scripts/test-console-core.mjs`、`scripts/test-console-monitor.mjs` 离线测试，页面交互仍需浏览器验证
- [`internal/app`](internal/app/) —— 编排核心（`serve` / `writeStream` / `aggregate` 路径）
- [`cmd/agent2api`](cmd/agent2api/) —— 启动装配、子命令、号池监视与保活
- [`cmd/console`](cmd/console/) —— 独立控制台的代理、路径与 SSE；已有离线测试，可补充异常网络场景

## 新增上游平台

架构上这是最受欢迎的一类贡献。多平台枢纽（Hub）已就绪，只需要实现适配器并注册：

1. 在 `internal/adapter/<平台名>/` 下新建包，实现 `adapter.Adapter` 接口（3 个方法：`Stream` / `ListModels` / `Name`）
2. 可选：实现 `adapter.Describer` / `adapter.Configurable`（用于控制台展示与热更新，不实现也能跑）
3. 注册以下装配点：
   - [`cmd/agent2api/accounts.go`](cmd/agent2api/accounts.go) 的 `newPlatformAdapter` 工厂（按平台构造适配器的分支）
   - [`cmd/agent2api/main.go`](cmd/agent2api/main.go) 的 `supportedPlatforms` 集合（启动校验）
   - [`cmd/agent2api/accounts.go`](cmd/agent2api/accounts.go) 的 `platformDefaultPaths`（该平台的默认凭证位置，自动探测用）
   - [`internal/config/config.go`](internal/config/config.go) 的平台字段继承规则；只有明确需要零配置自动启用时才修改 `autoPlatforms`，当前默认保持 WorkBuddy 两区
   - [`internal/app/hub.go`](internal/app/hub.go) 的 `platformDefaultBaseURLs`（该平台默认上游地址）
4. 根据平台实现认证、刷新及账号描述，并在 [`internal/adapter/setup.go`](internal/adapter/setup.go) 注册接入方式（设备授权、网页授权、API Key 或桌面登录态）与单账号 / 号池模式。控制台添加入口、平台页、账号详情、管理 API 和文档需要一起验证。`TestEveryImplementedPlatformHasDiscoverableOnboarding` 会检查所有已实现平台的入口，避免新增适配器后遗漏账号接入。其他认证方式不可落入 WorkBuddy 号池分支。

模型路由、`/v1/models` 合并、控制台多平台展示都由 Hub 通用处理，**三个下游协议编码器一行都不用改** —— 这是 IR 分层设计的核心收益。

> 若你计划接入新平台，欢迎先开 Issue 讨论。

## 代码风格

- 遵循 Go 惯例，`gofmt` 必须干净
- 注释用中文（与现有代码一致），但**注释要解释「为什么」，不是「做了什么」**
- 错误处理：本项目对错误分类有统一的 `llm.Failure` 结构，新代码请复用它而不是裸返回 `error`

## 提交规范

- 提交信息用中文或英文均可，建议用 `<类型>: <描述>` 前缀（`feat` / `fix` / `docs` / `test` / `refactor` / `chore`）
- 一个 PR 尽量只做一件事
- 大改动请先开 Issue 讨论，避免白做

## ⚠️ 切勿提交

以下路径已在 `.gitignore` 中，请**不要**用 `git add -f` 绕过：

| 路径 | 原因 |
|---|---|
| `config.json` | 可能含 `api_key` |
| `*.session.json`、`auths/`、`*.info` 及其临时文件 / 备份 | **认证凭证 —— 泄露即账号泄露** |
| `provider_config.json*`、`.zcode/` | 客户端密钥、配置与备份 |
| `agent2api-metrics.json`、`metrics.json` | 含真实调用统计（模型名、时间、token 数） |
| `.workbuddy/` | 内部开发记录 |
| `bin/` | 编译产物（11MB+，且含绝对路径） |

**提交 Issue 或 PR 时，同样请勿粘贴**真实的凭证、token、账号昵称或上游请求 ID。

## 关于许可证

本仓库**当前没有 LICENSE 文件**，即默认保留所有权利（详见 [README「许可证」](README.md#许可证)）。

在许可证确定前提交贡献，即表示你同意：你的贡献将以本项目最终选定的开源许可证发布。

## 有任何疑问

开一个 Issue 讨论即可。
