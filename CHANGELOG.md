# 更新日志

本文件记录本项目的重要变更。
格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 修复

- **号池加固**（参照 sub2api / CLIProxyAPI 两个成熟反代后逐项修正）：
  - **冷却只延长不缩短**：并发请求各自判定出不同时长时，后写者不再砍短已判定的限流窗口；顺带删掉一处恒假的死代码夹取、把 `time.Now()` 的三次调用收敛为一次
  - **首帧探针**：把「HTTP 200 但流内首发即报错」纳入换号窗口（这是 HTTP 状态层拒绝与流内失败之间的真实缺口）；已读到的非错误事件原样透传，探测不吞数据
  - **全池不可用语义三分解**：全部限流 → 429 + **最早**解冻秒数（原取最晚，白等已有容量）；全部鉴权失效 → 401 终态（原也返回 429，会让客户端无限重试一个永不成功的请求）
  - **限流指数退避**：上游未给重置时刻时按等级退避（10s 起，封顶 24h），且只在冷却窗口过期后才升级 —— 并发失败不会把等级一次冲顶；一次成功即清零
  - **5xx/传输失败加池级短退避 + 抖动**：避免上游整体故障时把 N 个账号无间隔连打（会被判定为压制重试）
  - 冷却状态从「一个时间戳」扩为 `ready/cooldown/blocked` + 原因枚举，供控制台展示
- **平台卡信息残缺**：号池模式下 `Describe()` 把上游地址/账号置空，控制台显示「上游 -」「账号 -」。现从账号适配器借真实信息，账号栏改为「N 个账号」

### 新增

- **`GET /api/accounts`**：暴露号池每个账号的运行状态（状态、冷却倒计时、最近错误、`is_next`）。单账号模式返回空数组，前端只有一条代码路径
- **控制台改版**（零构建约束下，参照成熟控制台的信息架构）：
  - 侧栏分组（运营 / 上游 / 工具）+ 内联 SVG sprite 图标 + 面包屑
  - **号池可视化**：平台页新增账号卡片区，标出「下一个使用」（▸）、冷却倒计时、状态徽标
  - **表格可用性**：滚动容器 + 表头 sticky（200 行日志滚到中间不再丢列名）；圆角与 overflow 从 `.table` 移到滚动容器，修掉窄屏静默裁列
  - **Toast 通知**：替换散落的 `showBanner`/侧栏文字 —— 设置页保存失败原本写进只存在于对话页 DOM 的 `#banner`，用户根本看不到
  - **表格三态**：加载骨架 / 空态（区分「没数据」与「筛选无结果」/ 错误态，原本三者共用一句「暂无记录」
  - 自动刷新：倒计时显示 + 切后台暂停 + 401 无视 silent（原来密钥失效时自动刷新静默失败，页面永远停在旧数据）
  - 键盘可达性（`:focus-visible`）、滚动条 hover 才显色、`::selection`、`prefers-reduced-motion` 降级（含流式光标特判）

### 新增

- **多账号号池**（解决「一个账号额度不够用」）：
  - `agent2api login -out auths/a.json` 逐账号攒凭证；网关启动扫描 `auths/`（自动启用）或 `-accounts-dir`/`accounts_dir` 指定的目录
  - `internal/adapter/pool.go`：通用的 `Pool`——包装 N 个同平台适配器，请求在健康账号间轮询；只依赖 `adapter.Adapter` 接口与 `llm.Failure` 分类字段，对 app 层就是一个普通 Adapter，下游协议零改动
  - 调度语义：限流→冷却该账号并换号（上游文案自带精确重置时刻，解析为**到点解冻**；解析失败退化 60s 冷却）；刷新后仍 401→冷却 10 分钟；参数错误不换号直接失败；传输错误换号但不冷却
  - 上游限流文案解析（「将在 YYYY-MM-DD HH:MM:SS UTC+8 重置」→ `RetryAfterSeconds`）
  - 启动横幅逐账号状态（✓/⏳）；全部冷却时返回语义化的 `all_accounts_cooling`
  - 回归测试 8 例（轮询均匀性、冷却换号、到期恢复、参数错误快速失败、传输错误不冷却、全冷却、空池、状态快照）

### 修复

- **全部 7 项已知缺陷已修复**（原列于本节与 README，回归测试全部锁定）：

- **Chat Completions 流式中断静默截断**：`EventError` 现发出 `{"error":{...}}` 数据帧（官方 openai-python SDK 的流内错误契约），错误后不再补发 `finish_reason`/`[DONE]`。
- **Anthropic 流式 `input_tokens` 恒为 0**：`message_delta.usage` 补发真实 input tokens（官方 SDK 按累积覆盖处理）。
- **Responses 协议 `output` 丢项**：`responseShell` 改按实际 key 遍历；「已 start 未 end」的块合成 `incomplete` 终态，不再静默消失。
- **工具描述未脱敏**：`convertTools` 接线 `SanitizeToolDescription`（sanitize 开关透传）。
- **无重试/退避**：dial 失败最多重试 3 次（指数退避 500ms→4s，尊重 Retry-After 但受 4s 安全阀钳制）；401 刷新每轮至多一次（防「refresh 恒成功而 chat 恒 401」的无界循环）；仅传输错误/5xx/429 重试。原 `isRetryableTransportError`/`parseRetryAfter` 死代码已接线。
- **控制台不带 API Key**：前端全链路（`api()` + 对话页流式请求）携带 `X-Api-Key`；401 时引导输入、验证通过才持久化；新增不鉴权的 `/api/auth-hint`（仅暴露布尔）；设置页新增密钥管理。
- **测试缺口**：新增 6 个测试文件；`llm` 88.6%、`common` 94.3%、`config` 75%、`chat` 53.1%、`app` 50.1%（此前 0%）。

- **错误分类修正**（伴随上述修复）：
  - 新增 `Failure.Unauthorized` 分类，凭据错误返回 **401**（原被 `ClientFixable` 统一映射为 400——客户端 SDK 靠 401 触发重新配置凭据）；上游 401 显式置位，不再依赖响应体文本。
  - 上游 429 显式置 `RateLimited`（原靠文本匹配，中文响应体会漏判成 502）。
  - `classify` 不再匹配裸数字 "401"/"429"——错误消息回显请求体片段（含 "user_401"）会把 `decode_failed` 误判成鉴权失败。
  - `Wrap` 对已是 `*Failure` 的输入补齐派生分类（幂等）：字面量经 Wrap 后派生字段不再全为零值。
  - 405 响应不再被映射成 400，且按 RFC 7231 带 `Allow: POST` 头。

## [0.2.0] - 2026-09-16

### 新增

- **指标持久化**：指标落盘并在重启后恢复，解决"刷新/重启后统计清零"问题
  - 新增 `-metrics-file` / `-no-persist` 参数与 `metrics_file` 配置项
  - 退出前（SIGINT/SIGTERM）自动落盘
- **网页控制台**：`go:embed` 编进二进制的管理面板，含概览 / 对话 / 平台 / 模型 / 调用日志 / 设置六个页面
  - 原生 SVG 图表，无前端构建步骤
  - 脱敏开关支持热更新
- **多平台预留结构**：`adapter.Describer` / `adapter.Configurable` 可选接口；`/api/platforms` 返回 `{active, planned}`
- **启动横幅**：直接打印控制台地址与全部接口 URL
- 指标趋势图补全为连续 30 分钟窗口（稀疏流量下不再近空）

### 修复

- **Anthropic 客户端整段回复渲染两遍**：流式编码器中内容块结束事件未从「未关闭」集合移除，导致收尾时重复补发 `content_block_stop`
- **`tool calls and tool results do not match`**：`convertMessages` 中工具结果为覆盖赋值，一条消息含多个工具结果时只保留最后一个；现展开为多条独立 tool 消息
- **Anthropic 协议图片 400**：直接附图缺 `data:` 前缀；`tool_result` 内的嵌套图片被忽略（Claude Code 读图场景）
- `/nope` 等未知路径错误返回 400，现正确返回 404
- 控制台字体显示问题（`.mono` 类缺失、时长格式、趋势图时间轴）

### 变更

- `llm.ToolResult` 新增 `Blocks []Content` 结构化字段，支持 `tool_result` 内嵌图片

## [0.1.0] - 2026-09-15

首个可用版本（M1）。

### 新增

- **IR 分层架构**：`internal/llm` 零依赖中间表示 + `adapter.Adapter` 接缝 + `ResponseStream` 单方法流抽象，把「N 平台 × M 协议」从 N×M 降为 N+M
- **三种下游协议**一次做全（均支持流式与非流式）：
  - `POST /v1/chat/completions`（OpenAI Chat Completions）
  - `POST /v1/responses`（OpenAI Responses）
  - `POST /v1/messages`（Anthropic Messages）
- **WorkBuddy 上游适配器**：
  - 凭证复用（直接读桌面客户端已登录凭证）+ 设备码登录双路
  - Token 过期自动刷新；401 时刷新重连
  - 强制流式（上游不支持非流式），非流式响应代理侧聚合
  - `delta.reasoning_content` → 思考过程映射
  - 工具调用分片按 index 缓存重组（含 `function.name` 回填）
  - `tool_choice` object → string 归一化
  - GBK / GB18030 兜底解码
  - 双重超时保护（空闲看门狗 + 总时长）
- **内容脱敏**：零宽字符插词 + harness 整段压缩，避免客户端固定模板被上游关键词审核误判
- **动态模型清单**：运行时从 `/v3/config` 拉取（带缓存与硬编码兜底），不再依赖会过期的内置清单
- **金帧回放测试**：真实上游样本离线回放，断言 IR 事件序列而非字节

### 修复

- **工具调用被文本吞掉**：上游 `tool_calls[].index` 从 0 开始，与 IR 内容块下标共用同一数字空间；当模型先输出文本再输出工具调用时两者冲突。现工具调用使用独立自增下标，上游 `index` 仅作分组键

---

[Unreleased]: ../../compare/v0.2.0...HEAD
[0.2.0]: ../../compare/v0.1.0...v0.2.0
[0.1.0]: ../../releases/tag/v0.1.0
