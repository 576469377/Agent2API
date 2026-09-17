# 贡献指南

感谢有兴趣参与本项目。请先阅读 [DISCLAIMER.md](DISCLAIMER.md) 与 README 的[已知限制](README.md#已知限制)章节。

## 环境要求

- **Go 1.23+**（`go.mod` 声明 `go 1.23.0`）
- 无需任何前端工具链（静态资源通过 `go:embed` 编进二进制）
- 依赖只有 `golang.org/x/text`（用于 GBK 兜底解码）

## 构建与测试

```bash
make build      # 编译到 bin/agent2api
make run        # 编译并运行
make test       # go test ./...
make vet        # go vet ./...
make fmt        # gofmt -w .
make check      # fmt + vet + test（提交前请跑这个）
make cover      # 覆盖率
make test-race  # 竞态检测（CI 也跑）
```

## 提交前的自查清单

- [ ] `make check` 通过
- [ ] 涉及协议转换的改动**必须**补/更新测试
- [ ] `gofmt` 已格式化（`make fmt`）
- [ ] **没有提交任何凭证**（见下方「切勿提交」）

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

当前真正偏低、欢迎优先补的（数字来自 `make cover`，会随代码漂移，动手前请以本地实测为准）：

- [`internal/web`](internal/web/) —— 控制台（**目前唯一完全无测试的包，0.0%**）
- [`internal/app`](internal/app/) —— 编排核心（`serve` / `writeStream` / `aggregate` 路径），约 38%
- [`cmd/agent2api`](cmd/agent2api/) —— 启动装配、子命令、号池监视与保活，约 7%

## 新增上游平台

架构上这是最受欢迎的一类贡献：

1. 在 `internal/adapter/<平台名>/` 下新建包
2. 实现 `adapter.Adapter` 接口（3 个方法：`Stream` / `ListModels` / `Name`）
3. 可选：实现 `adapter.Describer` / `adapter.Configurable`（用于控制台展示与热更新，不实现也能跑）
4. 在 [`cmd/agent2api`](cmd/agent2api/) 里注册

**三个下游协议编码器一行都不用改** —— 这是 IR 分层设计的核心收益。

> 注：当前 `cmd` 里对平台有硬编码判断（仅允许 `workbuddy`），接入新平台时需要一并调整。若你计划做这件事，欢迎先开 Issue 讨论。

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
| `*.session.json`、`auths/`、`*.info` | **认证凭证 —— 泄露即账号泄露** |
| `agent2api-metrics.json`、`metrics.json` | 含真实调用统计（模型名、时间、token 数） |
| `.workbuddy/` | 内部开发记录 |
| `bin/` | 编译产物（11MB+，且含绝对路径） |

**提交 Issue 或 PR 时，同样请勿粘贴**真实的凭证、token、账号昵称或上游请求 ID。

## 关于许可证

本仓库**当前没有 LICENSE 文件**，即默认保留所有权利（详见 [README「许可证」](README.md#许可证)）。

在许可证确定前提交贡献，即表示你同意：你的贡献将以本项目最终选定的开源许可证发布。

## 有任何疑问

开一个 Issue 讨论即可。
