# WorkBuddy 国际版接入

核对日期：2026-10-11。依据本机 WorkBuddy AI 客户端的 `cli/product.json`、加密存储实现，以及已登录账号的真实请求；下列内容没有账号凭证。

## 区域与接口

| 项目 | 国内版 | 国际版 |
|---|---|---|
| 平台 ID | `workbuddy` | `workbuddy-global` |
| 默认上游 | `https://copilot.tencent.com` | `https://www.workbuddy.ai` |
| 桌面凭证名 | `workbuddy-desktop.info` | `workbuddy-desktop-ai.info` |
| 独立账号目录 | `~/.workbuddy` | `~/.workbuddy-ai` |
| 设备登录标识 | `VSCode` | `workbuddy-ai` |

国际版继续使用 `/v3/config?repos=`、`/v2/chat/completions`、`/v2/plugin/auth/*` 和 `/v2/plugin/login/account`。国际版 `/console/chat/completions` 也已实测可用；代码仅在标准聊天路径返回 404/405 时回退，避免重放业务拒绝请求。

模型配置按 User-Agent 选择产品。国际版必须使用 `WorkBuddy/5.7.6`（本机客户端版本）；沿用国内适配器的 `CodeBuddyIDE/...` 会拿到另一个产品的 Auto/Fast/Balanced/Primary 等通用入口，遗漏实际 GPT、Gemini、Grok、GLM 等模型。已用本机登录态验证两个标识返回的目录不同；适配器已修正，并按媒体标签过滤图片/视频生成模型。

国际聊天必须以 system 消息开头，否则返回 `first message is not system prompt`。当标准 API 请求省略 system 时，适配器补一句普通助手提示；已有 system 保持不变。未指定模型使用 `default-model`。

## 加密凭证

新版客户端会把 token、昵称等字段存为 `{"$wbEncrypted":1,"envelope":"…"}`，其中 sym-v1 信封采用 AES-256-GCM，并验证字段 framing、suite 与 key ID。

适配器通过本机已安装客户端的原生存储能力读取密钥；只在进程内解密，不导出明文凭证、不硬编码密钥。刷新回写时保留原有 token 加密格式和其他客户端字段。未支持的加密 scheme、密钥不匹配或完整性失败均明确报错。

当前端到端验证环境为 Windows。默认探测 `%LOCALAPPDATA%/Programs/WorkBuddyAI/WorkBuddyAI.exe`；自定义安装位置可设置 `AGENT2API_WORKBUDDY_EXECUTABLE`。macOS/Linux 的安装路径可能不同，也可通过此变量指定；尚未在这两个系统进行加密凭证端到端验证。设备码登录产物仍兼容原有明文凭证格式。

国内和国际凭证候选、独立账号目录、号池、登录会话与上游地址隔离。账号域名与所选区域不一致时拒绝入池。国际模型枚举失败会返回错误，不发布国内版兜底快照。

## 实测结果

### 2026-10-22：会话字段缺失导致的生成拒绝

同一个已登录的国际版桌面账号和最小文本请求，原请求返回 HTTP 400、业务码 `11128`、`request illegal`；仅补齐 `X-Conversation-ID`、`X-Conversation-Request-ID`、`X-Conversation-Message-ID` 后返回正常 SSE 和最终回复。此处是请求兼容问题，不能据此认定账号封禁。已从本机 WorkBuddy AI 5.8.4 的请求构造确认这三个字段。

适配器在每次生成开始时建立会话元数据。已有会话路由键按平台、账号和键值派生 32 位十六进制会话标识，不向上游发送原始键；消息历史增长、适配器重新载入不改变该会话标识。无路由键时为单次调用生成独立会话标识。每次生成使用新的请求、消息标识，连接重试、令牌刷新和 404/405 兼容路由回退沿用本次标识，鉴权头仍读取当前凭证。

这保证请求携带稳定的会话字段，不代表已经命中上游缓存；缓存读写以实际 `usage` 为准。设备标识、账号权限、免费模型判断及限额处理保持原规则。

修复后逐个固定账号测试：三个国际版账号的 Hy3、DeepSeek V4.1 Flash、Step 5 Preview 共九次零积分请求全部成功；两个国内版账号的 Hy3 仍可正常回复。更新运行中的网关后，Chat Completions、Responses、Anthropic Messages 均返回正确文本；Messages 的流式工具调用、工具结果回传及最终回复完成了端到端验证。相关标识不写入公开实测记录。

### 首次接入记录

修正 WorkBuddy 产品标识后，当前国际账号返回 28 个模型条目，排除两个图片/视频模型后为 26 个聊天入口。包括 GPT-6.1-Sol、GPT-6 系列、GPT-5.6 系列、Gemini-3.5-Flash、Grok-4.7，以及与国内同名的 GLM、Hy3、DeepSeek 等。模型可用范围取决于账号及上游服务；没有把模式入口猜测成某个底层模型。

国际版 `workbuddy-global/glm-5.3` 已通过 Chat Completions / Responses / Anthropic Messages 的真实非流式请求，`workbuddy-global/gpt-6.1-sol` 已通过 Anthropic 流式请求。此前 `fast-model` 的工具调用已完成 `tool_use → tool_result → 最终文本` 往返。

国内账号的模型目录获取成功（15 个），本次 GLM 请求被上游以额度不足拒绝。启用同名回退后，裸名 `glm-5.3` 在三种协议下都成功转到国际版；固定 `workbuddy/glm-5.3` 仍返回国内原始额度错误。

## 同名模型路由

- 裸名按 `upstream.platforms` 声明顺序选择。国内优先时，原有 `glm-5.3` 等配置继续有效。
- `workbuddy/模型名` 和 `workbuddy-global/模型名` 明确固定平台，发给上游时移除平台前缀。
- `models.aliases` 可逐模型指定平台，例如 `"glm-5.3":"workbuddy-global/glm-5.3"`。
- `models.fallback_on_unavailable: true` 开启裸名同名回退：仅当建流前收到额度不足、限流或 HTTP 5xx 时，尝试其他平台目录内的同名模型。默认关闭；固定平台请求不回退；已经接受的流不切换平台。
- `/api/models` 保留各平台独立条目，提供 `routing_id`、`default_route` 与路由策略。`/v1/models` 中裸名唯一，平台限定名各自可调用。刷新模型清单同时重建归属索引，失败的平台保留最后有效目录。

## 启动和同步 ZCode

```powershell
go build -o bin/agent2api.exe ./cmd/agent2api
./bin/agent2api.exe -platform workbuddy-global
# 没有桌面登录态时：
./bin/agent2api.exe login -region global
```

也可声明两个平台同时运行：

```json
{"upstream":{"platforms":[
  {"id":"workbuddy","sanitize":true},
  {"id":"workbuddy-global","sanitize":true}
]}}
```

仓库脚本读取运行中网关的模型元数据，增量合并各平台限定名到同一供应商；保留已有模型、排序、启用状态、能力与推理设置及其他供应商，备份原文件并原子替换：

```powershell
python scripts/sync-zcode.py --config config.json
```

`--dry-run` 只写候选文件；`--default-model` 会同时设置 ZCode 默认供应商/模型。不带该参数时完整保留原默认选择。默认同步跳过缺失上下文或输出上限的条目；显式选择这些条目时拒绝写入，避免猜测元数据。

本机 ZCode 原有 15 个国内模型及其设置已从备份恢复，随后迁移为 `workbuddy/` 前缀，并新增 26 个 `workbuddy-global/` 国际限定名，合计 41 个。同步脚本的回归测试验证 dry-run、保留设置、同名隔离和重复运行幂等性。

本机配置已通过当前安装 ZCode 的原生 decoder/schema 校验。热加载是否成功应检查 `~/.zcode/v2/logs/<日期>.log`，详见 [ZCode 供应商配置报告](03-ZCode供应商配置逆向.md)。
