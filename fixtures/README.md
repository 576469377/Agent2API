# fixtures — 上游响应样本

本目录存放**脱敏后的真实上游响应样本**，用于协议转换的**离线金帧回放测试**。

测试时逐帧喂给解码器，断言产出的是 **IR 事件序列**而非字节，因此协议转换可以完全离线验证，不需要请求真实上游。

## 已脱敏

为保护账号隐私，以下字段已替换为**占位值**：

| 字段 | 说明 |
|---|---|
| `id` / `requestId` | 上游下发的请求与会话 ID，可回溯到具体账号 |
| `created` | 抓包时间戳，会暴露操作时间 |
| 工具调用 `id` | 同上 |
| 若干 UUID | 插件 / 市场产物 ID |

生成事件的内容结构与字段类型保留；标识值使用同类型占位值。TRAE 样本另按下文允许字段列表剔除私有元数据，不将数字标识改成字符串。

模型 ID（`glm-5.3`、`deepseek-v4.1-flash` 等）是上游公开的模型清单，未做处理。

## 文件

| 文件 | 内容 |
|---|---|
| `workbuddy-stream-text.jsonl` | 纯文本流式响应（750 帧） |
| `workbuddy-stream-toolcall.jsonl` | 工具调用流式响应，含分片 `arguments` |
| `workbuddy-stream-text-then-toolcall.jsonl` | ⭐ 文本 + 工具调用**并存**，用于回归「工具调用被文本吞掉」的下标冲突 bug |
| `workbuddy-v3-config.sample.json` | `/v3/config` 响应样本（模型清单 / 分级 / 阈值），目前仅作归档，无测试引用 |
| `doubao_sse_stream.txt` | 豆包 `/samantha/chat/completion` 的一次完整流式响应（8 帧：2002 开始 / 2010 意图 ×1 / 2001 增量×5 / 2003 结束）。**豆包平台已下线**，目前仅作归档，无测试引用 |
| `trae-stream-text.sse` | 2026-10-23 TRAE 原生 SSE；文本、思考、实际用量与 done |
| `trae-stream-toolcall.sse` | 同日真实原生工具流；function_call 与分片参数 |
| `trae-stream-tool-result.sse` | 工具结果续轮；实际输入 376，其中缓存读取 256，不重复累计 |
| `trae-free-quota.json` | 同日普通 Free 月赠 500 积分权益的最小脱敏结构；时间改为固定有效区间 |
| `trae-model-catalog.json` | 同日 `solo_agent` 目录的最小字段集；含 DeepSeek / GLM 的 dev、1M max 配对，Seed 256K 配对及会员专属排除样本 |
| `codex-namespaced-tools.json` | 2026-10-23 实际 Codex 客户端请求中函数与 MCP 命名空间的最小结构；保留工具层级和参数类型，提示词与模型名替换为测试值；用于下游编解码回归，并非上游响应 |

TRAE 样本仅保留解析所需字段，移除账号 / 设备、会话和 pack 标识、内部提示词、加密模型参数。`timing_cost` 和携带缓存材料的 `extra_info` 帧整体省略。`prompt_completion_id` 使用数字占位，工具调用 ID 使用字符串占位；正文是固定测试提示的响应，不含用户项目内容。授权凭据与原始抓包只留在被忽略的本机目录。

## ⚠️ 贡献提醒

**请勿用未经处理的真实抓包覆盖这些文件** —— 会重新引入个人数据。

若上游协议有变更需要更新样本，请先按上表脱敏，再提交；并确保 `go test ./internal/adapter/workbuddy/ -run TestReplay` 仍通过。

`trae-checkin-status.json` 为 2026-10-23 官方签到状态接口的只读响应，保留 enable / checked_in / credits 的实际类型与奖励数值；不含账号、设备或认证材料。领取与异常分支在测试中使用合成响应，未将其冒充真实领取金帧。

Kiro 离线样本：`kiro-free-usage.json` 为合成 Free 月度额度与关闭按量计费结构（日期和余额为测试值）；`kiro-model-catalog.json` 保留已验证账号目录的公开模型、上下文和真实积分倍率。均不含账号、令牌、会话或原始私有请求。ACP / MCP 续接、取消和付费拦截通过隔离的 CLI 子进程测试回放。
