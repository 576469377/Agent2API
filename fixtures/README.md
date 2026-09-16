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

**结构与文本内容保持原样**（消息体、工具 schema、字段顺序均未改动），因此协议证据完整有效。

模型 ID（`glm-5.3`、`deepseek-v4.1-flash` 等）是上游公开的模型清单，未做处理。

## 文件

| 文件 | 内容 |
|---|---|
| `workbuddy-stream-text.jsonl` | 纯文本流式响应（750 帧） |
| `workbuddy-stream-toolcall.jsonl` | 工具调用流式响应，含分片 `arguments` |
| `workbuddy-stream-text-then-toolcall.jsonl` | ⭐ 文本 + 工具调用**并存**，用于回归「工具调用被文本吞掉」的下标冲突 bug |
| `workbuddy-v3-config.sample.json` | `/v3/config` 响应样本（模型清单 / 分级 / 阈值），目前仅作归档，无测试引用 |
| `doubao_sse_stream.txt` | 豆包 `/samantha/chat/completion` 的一次完整流式响应（8 帧：2002 开始 / 2001 增量×5 / 2003 结束） |

## ⚠️ 贡献提醒

**请勿用未经处理的真实抓包覆盖这些文件** —— 会重新引入个人数据。

若上游协议有变更需要更新样本，请先按上表脱敏，再提交；并确保 `go test ./internal/adapter/workbuddy/ -run TestReplay` 仍通过。
