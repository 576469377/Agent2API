# 更新日志

本文件记录本项目的重要变更。
格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 已知问题（尚未修复）

按影响排序，详见 README 的[已知限制](README.md#已知限制)：

- Chat Completions 流式中断时**静默截断**（无 `finish_reason`、无 `[DONE]`、无错误帧）
- Anthropic 流式 `input_tokens` 恒为 `0`
- Responses 协议在稀疏内容下标下可能**丢失 `output` 条目**
- `SanitizeToolDescription` 已实现但**从未被调用**（工具描述未脱敏）
- **无任何重试 / 退避**（仅 401 刷新重连）
- 网页控制台**不携带 API Key**，开启鉴权后 `/api/*` 全部 401
- 编排核心、Chat 编解码器、配置层**无测试**

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
