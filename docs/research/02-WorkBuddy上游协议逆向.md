# WorkBuddy / CodeBuddy 上游协议逆向报告

> 调研日期：2026-09-15
> **证据等级说明**：
> - 🟢 **实测** = 本机真实请求验证通过，附实测原文
> - 🟡 **推断** = 来自参考实现源码，未验证
> - ⚪ **未确认** = 参考实现亦无，需后续抓包

---

## 一、端点总览

Base URL：[copilot.tencent.com](https://copilot.tencent.com)
（🟢 实测：`GET /v3/config` 返回的 `data.endpoint` 字段值就是这个字符串）

| 用途 | Method | Path | 证据 |
|---|---|---|---|
| **聊天（唯一 LLM 端点）** | POST | `/v2/chat/completions` | 🟢 实测 200 |
| **模型清单 / 远程配置** | GET | `/v3/config?repos=` | 🟢 实测 200（36,900 字节） |
| 刷新 Token | POST | `/v2/plugin/auth/token/refresh` | 🟡 源码 + 日志中出现 6 次 |
| 账号信息 | GET | `/v2/plugin/accounts` | 🟢 实测 200 |
| 设备码登录（取授权 URL） | POST | `/v2/plugin/auth/state?platform=` | 🟡 源码 |
| 设备码登录（轮询 token） | GET | `/v2/plugin/auth/token?state=` | 🟡 源码 |
| 设备码登录（轮询账号） | GET | `/v2/plugin/login/account?state=` | 🟡 源码 |
| 额度/积分 | GET | `/v2/billing/meter/get-user-resource` | 🔴 **实测 404**，见第八节 |
| 遥测上报 | POST | `/v2/report` | 🟡 日志中出现 55 次（非必需） |

**注意**：上游是**无状态的**，没有「创建会话」端点，也没有 session_id / conversation_id 字段。多轮对话靠客户端每次重发完整 `messages` 数组。

---

## 二、认证

### 2.1 凭证来源（🟢 实测）

桌面客户端凭证路径：

```
~/Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info
```

JSON 结构（实测字段名）：

```json
{
  "auth": {
    "accessToken":   "<JWT, 1527 字符>",
    "refreshToken":  "<JWT, 1528 字符>",
    "tokenType":     "Bearer",
    "expiresIn":     <number>,
    "expiresAt":     <毫秒时间戳>,
    "refreshExpiresAt": <毫秒时间戳>,
    "lastRefreshTime":  <毫秒时间戳>,
    "sessionState": "<uuid>",
    "domain":       "www.workbuddy.cn"
  },
  "account": {
    "uid": "<uuid>", "nickname": "...", "uin": "...",
    "type": "personal", "pluginEnabled": true,
    "phoneNumber": "...", "mpOpenId": "..."
  },
  "accounts":    [ { ...同上 account... } ],
  "allAccounts": [ { ...同上 account... } ]
}
```

⚠️ `expiresAt` / `lastRefreshTime` 是**毫秒**时间戳，不是秒。

> 参考实现（hawklithm）用的是**自己登录后存的** `~/.codebuddy-session.json`，结构与上不同。
> **我们应当优先直接复用桌面客户端已有的 `workbuddy-desktop.info`**——用户无需重新登录。这一点是所有参考实现都没做的。

### 2.2 请求头清单（🟢 实测通过的最小集合）

```http
Authorization:      Bearer <accessToken>
User-Agent:         Mozilla/5.0 (compatible; Genie-IDE/1.0)
Content-Type:       application/json
Accept:             text/event-stream
X-Product-Code:     codebuddy
X-IDE-Type:         vscode
X-IDE-Name:         Visual Studio Code
X-IDE-Version:      1.70.2
X-Product-Version:  4.10.33259736
X-Machine-Id:       <稳定 uuid>
X-User-Id:          <account.uid>
X-Domain:           <auth.domain 或 www.workbuddy.cn>
```

企业版账号额外需要（🟡 源码，实测账号为 personal 类型未触发）：

```http
X-Enterprise-Id:    <account.enterpriseId>
X-Tenant-Id:        <account.enterpriseId>
X-Department-Info:  <account.departmentInfo>
X-Refresh-Token:    <auth.refreshToken>    # 仅刷新请求
```

**实测结论**：`X-Machine-Id` 可以随便填一个稳定 uuid（实测固定值可通过），不校验。`X-User-Id` / `X-Domain` 也非强制（但建议带上，减少被判定为异常客户端的风险）。

### 2.3 刷新逻辑（🟡 源码）

```
if accessToken 存在 且 expiresAt > now + 60_000:  直接用
else: POST /v2/plugin/auth/token/refresh
      headers = auth_headers(access=false, refresh=true) + {"X-Auth-Refresh-Source":"plugin"}
      body = {}
      失败 → 重新走浏览器登录
```

另：上游返回 401 时，参考实现会 refresh 一次后重试原请求。

---

## 三、请求体（`POST /v2/chat/completions`）

**核心事实（🟢 实测）：上游直接吃 OpenAI Chat Completions 格式的 JSON。**

最小可用请求（🟢 实测 200）：

```json
{
  "model": "default",
  "messages": [{"role": "user", "content": "hi"}],
  "stream": true,
  "stream_options": {"include_usage": true}
}
```

### 3.1 字段说明

| 字段 | 类型 | 说明 |
|---|---|---|
| `model` | string | 上游模型 id。**传 `default` 会被解析成真实模型**（实测返回 `glm-5.3`） |
| `messages` | array | `[{role, content}]`，`content` 可为 string 或 `[{type:"text",text:""}]`。`assistant` 可带 `tool_calls`；`tool` 角色带 `tool_call_id` |
| `stream` | bool | **必须 true**，见第四节 |
| `stream_options` | object | `{"include_usage": true}` 才会返回 usage |
| `tools` | array | OpenAI 形态。`parameters` 必须是非空 dict 且**必须含 `type` 字段**，否则上游 400（🟡 源码） |
| `tool_choice` | **string** | ⚠️ 见 3.2 |
| `temperature`/`top_p`/`top_k`/`stop`/`seed`/`presence_penalty`/`frequency_penalty`/`response_format`/`reasoning_effort` | — | 透传（🟡 源码；`reasoning_effort` 是否被上游接受 ⚪ 未确认） |
| `max_tokens` | int | 透传 |

### 3.2 ⚠️ 坑：`tool_choice` 必须是 string

上游 Go 后端的 `tool_choice` 是 **string 类型**，OpenAI 标准的 object 形式会触发 400：

```
cannot unmarshal object into Go struct field Request.tool_choice of type string
```

归一化规则（🟡 源码 `test_tool_choice_normalize.py`）：

| 输入 | 输出 |
|---|---|
| `{"type":"function","function":{"name":"X"}}` | `"X"` |
| `{"type":"function"}`（缺 name） | `"required"` |
| `"auto"` / `"none"` / `"required"` | 原样 |

### 3.3 上游不支持非流式（🔴 关键）

🟢 实测，`"stream": false` 返回：

```json
{
  "code": 11101,
  "msg": "Non-stream chat request is currently not supported",
  "requestId": "00000000-0000-4000-8000-000000000002",
  "displayMsg": {
    "en": "Invalid request parameters. Please check and retry.",
    "zh": "请求参数有误，请检查后重试。",
    "zh-hant": "請求參數有誤，請檢查後重試。"
  }
}
```

→ **代理必须始终以流式请求上游，非流式客户端的响应在代理侧本地聚合。**

---

## 四、流式响应格式

### 4.1 帧格式（🟢 实测）

- **只有 `data:` 行，没有 `event:` 行**
- 终止帧是 `data: [DONE]`
- 每行一个完整 JSON

### 4.2 Chunk 完整结构（🟢 实测原文）

```json
{
  "id": "cmb-00000000000000000000000000000004",
  "model": "glm-5.3",
  "object": "chat.completion.chunk",
  "created": 1700000004,
  "choices": [{
    "index": 0,
    "delta": {
      "role": "assistant",
      "content": "",
      "reasoning_content": "Let me consider how to respond",
      "function_call": null,
      "refusal": "",
      "tool_calls": [],
      "extra_fields": null
    },
    "logprobs": null,
    "finish_reason": ""
  }],
  "usage": null
}
```

### 4.3 ⭐ 关键发现：`reasoning_content`（思考过程）

**`delta.reasoning_content` 就是思考过程字段。** 这是本次调研最重要的发现之一——

三个参考实现**全都没有处理这个字段**（hawklithm 的 `__main__.py` 只取 `delta.content`），导致思考过程被静默丢弃。我们要做对：映射到 OpenAI 的 `reasoning_content` / Anthropic 的 `thinking` block / Responses 的 reasoning summary。

### 4.4 工具调用（🟢 实测原文）

首片带 `id` + `name`，后续片 `name` 为空、只带 `arguments` 分片：

```json
{"delta":{"tool_calls":[{"id":"call_0000000000000000000005","type":"function",
  "function":{"name":"get_weather","arguments":""},"index":0}]}, "finish_reason":""}
{"delta":{"tool_calls":[{"type":"function",
  "function":{"name":"","arguments":"{\"city\": \"北京\""},"index":0}]}, "finish_reason":""}
{"delta":{"tool_calls":[{"type":"function",
  "function":{"name":"","arguments":"}"},"index":0}]}, "finish_reason":"tool_calls"}
```

→ **必须按 `index` 缓存 `function.name`，给后续空 name 的分片回填。**

另有 `delta.function_call: {"name":"","arguments":""}` 字段（legacy 形态），实测在非工具调用流的末帧会出现空值对象，可忽略。

### 4.5 `finish_reason`（🟢 实测）

- 中间帧是**空字符串 `""`**（不是 null！）→ 严格客户端会序列化失败，**必须删除该字段而非置 null**
- 末帧是 `"stop"` 或 `"tool_calls"`

### 4.6 usage（🟢 实测）

只在**最后一个 chunk**（含 finish_reason 的那帧）出现：

```json
"usage": {
  "prompt_tokens": 157,
  "completion_tokens": 55,
  "total_tokens": 212,
  "completion_tokens_details": {
    "accepted_prediction_tokens": 0,
    "audio_tokens": 0,
    "reasoning_tokens": 36,
    "rejected_prediction_tokens": 0,
    "cached_tokens": 0
  }
}
```

⚠️ 注意：实测中 **prompt_tokens 157 / completion_tokens 55 明显偏小**（一个简单带工具的对话），推测上游统计口径与真实 token 数不一致。做计费/限额时不能直接信任。

---

## 五、DSML：文本态工具调用兜底（🟡 源码，未复现）

当模型以**文本标记**而非原生 `tool_calls` 输出工具调用时，标记会混在 `delta.content` 里。这就是 hawklithm 花 983 行写的 `dsml_parser.py` 在解析的东西。

### 5.1 标记前缀（三种变体，含全角）

```
｜｜DSML｜｜     ← 全角竖线 U+FF5C
||DSML||       ← 半角双竖线
|DSML|         ← 半角单竖线
```

必须做**全角→半角归一化**（`U+FF01..U+FF5E` 减 `0xFEE0`）。

### 5.2 标签

- `<tool_calls>` / `<tool_call>` / `<tool-calls>` / `<toolcalls>` — 外层容器
- `<invoke name="X">` — 单次调用
- `<parameter name="Y" string="true">` 或 `<Y>` — 参数（任意标签名都接受）

### 5.3 参数编码优先级

1. **CDATA**（最高，不做 HTML 解码）：`<param><![CDATA[...]]></param>`
2. **DSML parameter 标签**：取 `name` 属性
3. **简单标签**：参数名 = 标签名

非 CDATA 值一律 `html.unescape()`。最终 `json.dumps()` 成 `arguments`。

### 5.4 必须跳过的区域

Markdown 围栏（``` / ~~~）、内联 code span（反引号）、CDATA、`<!-- -->`、`<? ?>`。被这些区域包裹的 `<invoke>` **不是**工具调用。

### 5.5 示例（源码原文）

```
<｜｜DSML｜｜tool_calls><｜｜DSML｜｜invoke name="bash"><｜｜DSML｜｜parameter name="cmd">ls -la</｜｜DSML｜｜parameter></｜｜DSML｜｜invoke></｜｜DSML｜｜tool_calls>
```

### 5.6 流式缓冲语义

- 见到完整闭合的 `<tool_calls>…</tool_calls>` 或 `<invoke>…</invoke>` 才产出工具调用，返回其**前缀**文本
- 未闭合时只扣留尾部（从最后一个 `<` 起），避免吞掉正常文本
- 流结束 `flush()` 时残留**原样作为纯文本**吐出，保证零丢失

### 5.7 ⚠️ 不要覆盖原生 tool_calls

上游可能**同时**下发原生 `tool_calls` 和含 `<invoke>` 的 content（源码 `test_bug_b2_simulation.py` 记录了此 bug）。
→ **有原生 `tool_calls` 时，DSML 解析结果不得覆盖它。**

**我们的判断**：这是历史遗留兜底通道。建议第一版**实现但默认关闭**（配置项开关），因为实测几次都走原生通道。保留代码路径以防上游回退。

---

## 六、模型清单（🟢 实测，`/v3/config`）

`GET /v3/config?repos=` 返回 `data.models`（22 个）：

| id | name | 输入 | 输出 | 图像 | 工具 | 思考 | 默认 |
|---|---|---:|---:|:-:|:-:|:-:|:-:|
| `auto` | Auto | 168000 | 32000 | Y | Y | Y | **Y** |
| `hy4-preview-f` | Hy4 preview | 1000000 | 64000 | Y | Y | Y | |
| `hy3` | Hy3 | 192000 | 64000 | Y | Y | Y | |
| `hy3-x` | Hy3 | 192000 | 64000 | Y | Y | Y | |
| `deepseek-v4.1-flash` | Deepseek-V4.1-Flash | 1000000 | 393216 | Y | Y | Y | |
| `deepseek-v4-pro` | Deepseek-V4-Pro | 1000000 | 393216 | Y | Y | Y | |
| `glm-5.3` | GLM-5.3 | 1000000 | 131072 | Y | Y | Y | |
| `glm-5.3-flash` | GLM-5.3-Flash | 1000000 | 131072 | Y | Y | Y | |
| `glm-5.2` | GLM-5.2 | 1000000 | 131072 | Y | Y | Y | |
| `glm-5.1` | GLM-5.1 | 200000 | 48000 | Y | Y | Y | |
| `glm-5v-turbo` | GLM-5v-Turbo | 200000 | 131072 | Y | Y | Y | |
| `kimi-k3-1` | Kimi-K3 | 1000000 | 1048576 | Y | Y | Y | |
| `kimi-k2.7` | Kimi-K2.7-Code | 256000 | 262144 | Y | Y | Y | |
| `kimi-k2.6` | Kimi-K2.6 | 256000 | 262144 | Y | Y | Y | |
| `minimax-m3` | MiniMax-M3 | 512000 | 524288 | Y | Y | Y | |
| `deepseek-v4-flash` | Deepseek-V4-Flash | 1000000 | 50000 | Y | Y | Y | |
| `codewise-default-model-v2` | Default | 96000 | 32000 | Y | Y | N | |
| `codewise-completions` | — | — | 256 | N | N | N | 代码补全专用 |
| `codewise-rewrite` | — | — | 256 | N | N | N | 代码补全专用 |
| `nes-gf` | — | — | 256 | N | N | N | 代码补全专用 |
| `codewise-jump` | — | — | 256 | N | N | N | 代码补全专用 |
| `hunyuan-image-alpha` | Hunyuan Image Alpha | — | — | N | N | N | 图像生成 |

**建议对外暴露时过滤掉** `codewise-*` / `nes-gf` / `hunyuan-image-alpha`（不是聊天模型）。
实际聊天模型约 **16 个**。

### 6.1 `/v3/config` 的其它有用字段

- `data.endpoint` → `"https://copilot.tencent.com"`
- `data.modelTiers` → 订阅分级（含 `modelIds` 列表）
- `data.tokenUsageThresholds` → 上下文压缩阈值

### 6.2 ⚠️ 请求 `/v3/config` 必须用特定 UA

🟡 源码注释（hawklithm `record_codebuddy_real_fixtures.py:65`）：

> The VSIX sends a CodeBuddy IDE UA. The config service rejects a generic Python/curl UA with code **12403 ("check ua")**.

需要的头：
```http
User-Agent:      CodeBuddyIDE/4.10.33259736
X-IDE-Type:      VSCode
X-IDE-Name:      VSCode
X-Product:       SaaS
X-Requested-With: XMLHttpRequest
Accept:          application/json
```

**注意这里的 `X-IDE-Type`/`X-IDE-Name` 是大写 `VSCode`，与聊天请求的 `vscode` / `Visual Studio Code` 不同。**

---

## 七、脱敏（desensitize）—— 刚需

### 7.1 为什么必须做

上游有**关键词级内容审核**。Claude Code / Codex CLI 的固定 system 模板里包含大量合规声明词（例如「Refuse requests for DoS attacks, exploit development, credential testing...」），这些是**拒绝作恶的声明**，却被后端误判为有害内容，导致整条请求被拦。

hawklithm README 原文：不启用脱敏时「**几乎每次请求都会被审核拦截**」。

失败表现：`{"error":{"message":"内容违规","type":"content_policy_violation"}}` 或空响应/连接中断。

### 7.2 具体手段（🟡 源码，约 100 个词）

1. **零宽空格插词**：`"DoS"` → `"Do\u200bS"`（插在第 1 个字符后），正则按词长降序 + IGNORECASE
2. **词表分类**：
   - 攻击类：`DoS` `DDoS` `exploit` `credential testing` `brute force` `privilege escalation` `reverse shell` `SQL injection` `XSS` `malware` `ransomware` `zero-day` ...
   - 安全术语：`vulnerability` `penetration testing` `cybersecurity` `attack` `injection` ...
   - 有害内容：`harmful` `dangerous` `weapon` `bomb` ...
   - **品牌词**：`Claude Code` `Claude Opus` `Anthropic` `Co-Authored-By` ...
   - **身份特征词**：`Oh My Pi` `Codex CLI` `coding harness` `subagent` `MCP Server` `tool call` `antml:invoke` ...
   - **内部协议 URI**：`skill://` `agent://` `artifact://` `memory://` ...
3. **整块替换**：`<environment_context>` / `<permissions instructions>` / `<collaboration_mode>` / `<skills_instructions>` / `<plugins_instructions>` 五个块整段换成一句话摘要
4. **Harness 语义压缩**（最激进）：命中 Claude harness（长度 ≥1000 且特征词命中 ≥2）→ 整段替换为摘要。
   源码注释：零宽空格对这一块**无效**，必须整段压掉。

**作用范围**：只处理 `role:"system"`；`user`/`assistant` 不改。

⚠️ 源码里 `tools[].description` 的脱敏函数定义了但**从未被调用**（README 声称已处理，与代码不符）。我们实现时应补上或明确不做。

---

## 八、额度查询（🔴 未解决）

| 尝试 | 结果 |
|---|---|
| `copilot.tencent.com/v2/billing/meter/get-user-resource` | 404 |
| `www.codebuddy.cn/v2/billing/meter/get-user-resource` | 404 |
| `copilot.tencent.com/console/billing/meter/get-user-resource` | **403 `{"error":"access_denied","error_description":"not_authorized"}`** |

**结论**：`/console/billing/meter/*` 路由**存在**但拒绝访问（可能需企业版权限）。Sliverkiss 注释提到 global realm 走 `workbuddy.ai /billing/meter/*`。

→ **额度查询不是核心链路，第一版不做。** `/v3/config` 里每个模型带的 `credits` 字段（形如 `"x1.20"`）是**积分倍率**而非余额，别搞混。

---

## 九、其它上游怪癖清单（🟡 源码）

| # | 怪癖 | 处理 |
|---|---|---|
| 1 | `tool_choice` 是 string | object → 函数名字符串 |
| 2 | `finish_reason` 返回 `""` | 删除字段（不是置 null） |
| 3 | tool_calls 后续分片 name 为空 | 按 index 缓存回填 |
| 4 | 偶发返回 GBK 字节 | 响应解码 `utf-8 → gbk → cp936 → latin-1` 兜底 |
| 5 | 请求体也可能非 UTF-8 | 同上，解码失败返回结构化 400 而非 500 |
| 6 | 流可能无限期运行（实测过 6+ 分钟 2000+ chunks） | 空闲超时 + 总时长超时双重保护 |
| 7 | 上游 400 常与 tools 定义相关 | 400 时记录 tools 样本到日志 |
| 8 | 账户信息异步就绪 | 登录后轮询最多 60s |
| 9 | `/console/chat/completions` 与 `/v2/chat/completions` 双路径分叉 | Sliverkiss：global 先打前者，404/405 回落后者 |
| 10 | `tools[].parameters` 必须含 `type` | 过滤掉不合规的 tool 定义 |
| 11 | **`tool_calls[].index` 从 0 开始，会与文本块下标撞号** | 见下方「实测踩坑」，必须用独立下标空间 |

### 9.1 实测踩坑：工具调用被文本吞掉（已修复）

**现象**：同一条带工具的请求，有时返回 `tool_calls` 正常，有时返回 `finish_reason=tool_calls` 但 `tool_calls` 为 null。

**根因**：上游 `tool_calls[].index` 从 0 开始计数，与代理内部「内容块下标」共用同一个数字空间。当模型**先输出一段文本**（占下标 0）、**再输出工具调用**（index 也是 0）时，两者的下标冲突，聚合阶段工具调用被文本块覆盖。

**为什么难发现**：模型是否先输出 preamble 文本是随机的（实测 4 次里 3 次有），所以表现为「时好时坏」，很容易被误判成上游抖动。

**修复**：工具调用的 IR 内容下标用独立的自增计数器分配，与上游 `index` 解耦；上游 `index` 仅用于把分片归组。

**对应的回归测试**：[`internal/adapter/workbuddy/sse_test.go`](../../internal/adapter/workbuddy/sse_test.go) 的 `TestReplayTextThenToolCall`，用 [`fixtures/workbuddy-stream-text-then-toolcall.jsonl`](../../fixtures/workbuddy-stream-text-then-toolcall.jsonl)（专门录制的「文本 + 工具调用并存」真实流）做金帧回放，断言文本块与工具调用块的下标不相等。

> 教训：上游的 `index` 这类「数组下标」字段，只能当分组键用，不能直接当全局内容块标识。

---

## 十、待确认清单（⚪）

按优先级排序，后续抓包或实测补充：

1. **思考过程的开关**：`reasoning_content` 是否受 `reasoning_effort` / 模型 `onlyReasoning` 控制？
2. **多模态输入**：`supportsImages: true` 的模型，图片怎么传（`image_url` base64？）
3. **`extra_fields`**：chunk 里的 `delta.extra_fields` 是 null，什么情况下有值？
4. **额度端点**：见第八节
5. **限流行为**：触发限流时的状态码与响应体（推测 429，未实测）
6. **context length 超限**：错误码与响应体
7. **`/v2/chat/completions` 是否接受 `response_format: {type:"json_object"}`**

---

## 附：复现脚本

调研期间使用的探测脚本位于 `/tmp/wb_probe/probe.py`（临时目录，未纳入版本库）。
建议后续把探测能力固化成项目内的 `cmd/probe` 工具（参考 devin2api 的做法），便于协议漂移检测。

原始 `/v3/config` 响应已归档：[`fixtures/workbuddy-v3-config.sample.json`](../../fixtures/workbuddy-v3-config.sample.json)
