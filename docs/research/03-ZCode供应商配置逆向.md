# ZCode 供应商配置逆向报告

> 调研日期：2026-10-02
> **证据等级说明**：
> - 🟢 **实测** = 本机真实操作验证通过（本报告绝大多数结论如此，含一次实机故障的完整定位）
> - 🟡 **推断** = 从客户端代码（zod schema）逆向得出，未逐项实测
>
> 背景：把 Agent2API（本机网关）配置成 ZCode 桌面客户端的模型供应商，并同步全部模型的元数据。
> 期间因 schema 严格校验连续踩坑三次，导致「用户自有供应商在 UI 全部消失」的实机故障——
> 本文记录完整文件结构、必填字段与排错路径，避免后来者重蹈。

⚠️ **安全提示**：`provider_config.json` 内含各供应商的**明文 API Key**，排错时切勿把真实文件内容截图/粘贴到公开场合。

---

## 1. 两种配置路径

| 路径 | 适用 | 说明 |
|---|---|---|
| **UI**（推荐） | 添加供应商、选模型 | 设置 → 供应商 → 添加自定义供应商；填协议 / Base URL / API Key，模型逐个添加 |
| **直接编辑文件** | 批量配模型、配模型元数据（上下文窗口等） | `~/.zcode/v2/provider_config.json`，本文主角 |

客户端对文件**每 60 秒轮询热加载**：改对了一分钟内生效（无需重启）；改错了**静默降级**（见 §5）。

## 2. 文件结构与位置

```
~/.zcode/v2/provider_config.json          个人供应商配置（本文主角，可编辑）
~/.zcode/v2/setting.json                  客户端主设置（选中项等，一般不用动）
~/.zcode/v2/runtime/provider/<平台>/<版本>/endpoint-*/zcode-builtin.json
                                          内置模板（只读勿改，但是现成的字段范例库）
~/.zcode/v2/logs/<日期>.log               客户端主进程日志（排错关键，见 §5）
```

文件顶层结构（🟢 实测；`modelConfigRules` 与 `providerConfigRules` 是**平级**的，见 §4 坑三）：

```json
{
  "schemaVersion": 1,
  "config": {
    "providerOrder": ["deepseek", "bigmodel-api", "agent2api"],
    "providerConfigRules": { "providerRules": [ /* 供应商条目，见 §3 */ ] },
    "modelConfigRules":   { "providerModelRules": [], "manualProviderModelRules": [ /* 模型元数据，见 §4 */ ] }
  }
}
```

## 3. 供应商条目（providerRules）

```json
{
  "providerId": "agent2api",
  "providerName": "Agent2API",
  "enabled": true,
  "config": {
    "group": "standard-personal",
    "access": { "type": "api-key", "apiKey": "<密钥或占位符>" },
    "api": { "type": "anthropic-messages", "baseUrl": "http://127.0.0.1:8787/v1" },
    "personalModelIds": ["glm-5.3", "hy3", "..."],
    "modelOrder": ["glm-5.3", "hy3", "..."]
  }
}
```

要点（🟢 实测 + 🟡 schema 逆向）：

- schema 是 zod **`.strict()`**：字段名错一个、多一个，**整份配置文件被拒**（不只这一条）
- 用户级规则**不能**带 `builtinModelIds`（那是内置模板专用）；自定义模型放 `personalModelIds`
- `providerId` 以 `account:` 开头的是内置固定账户（如 `account:bigmodel-start-plan`），其 access 不可写
- `api.type` 与请求路径的拼接规则：`anthropic-messages` = baseUrl + `/messages`（**baseUrl 要带 `/v1`**）；`openai-chat-completions` = baseUrl + `/chat/completions`；`openai-responses` = baseUrl + `/responses`
- 接 Agent2API 用 `anthropic-messages`（与 Claude Code 同链路，工具/思考支持最完整）

## 4. 模型元数据（modelConfigRules）

**不给元数据时，ZCode 对自定义模型按默认 200K 上下文处理**——不只显示错，还会提前触发会话压缩。
真实值从网关 `/api/models` 的 `context_tokens` / `max_output_tokens` 同步。

两条数组的分工：`providerModelRules` 针对**模板型**供应商（config 全字段可省）；
`manualProviderModelRules` 针对**手动型**供应商（自定义网关走这里，**字段大量必填**）。

```json
{
  "providerId": "agent2api",
  "modelId": "space-bunny",
  "config": {
    "enabled": true,
    "properties": {
      "contextWindow": 1000000,
      "supportsJsonSchemaOutput": false,
      "supportsNativeWebSearch": false,
      "supportsMidConversationSystem": false,
      "inputFormat": { "supportsImage": true, "supportsVideo": false, "supportsPdf": false }
    },
    "optionSpecs": {
      "reasoningLevel": {
        "values": ["disabled", "enabled"],
        "map": "reasoningLevel == \"disabled\" ? {\"thinking\":{\"type\":\"disabled\"}} : {\"thinking\":{\"type\":\"adaptive\"},\"output_config\":{\"effort\": reasoningLevel == \"enabled\" ? \"high\" : reasoningLevel}}"
      },
      "maxOutputTokens": { "max": 128000 }
    }
  }
}
```

必填清单（🟡 逆向自 `zcode.cjs` 的 zod 定义；漏一个 = 整份文件被拒）：

- `properties`：`contextWindow`(正整数)、`supportsJsonSchemaOutput`、`supportsNativeWebSearch`、`supportsMidConversationSystem`、`inputFormat{supportsImage, supportsVideo, supportsPdf}` —— **全部必填**
- `optionSpecs`：`reasoningLevel{values, map}` 与 `maxOutputTokens{max}` 整体必填
  - `reasoningLevel.map` 是**表达式 DSL 字符串**（运行时编译求值），照抄内置范例最稳：anthropic-messages 用上行写法（`values: ["disabled","enabled"]`）
  - **`maxOutputTokens` 在模型级只允许 `{max}` 一个键**——内置文件里 `providerSiteRules` / `modelApiRules` 范例带 `map`，那是 API 级 schema，混用必炸（🟢 实测踩坑）
- 两条数组不得声明同一 `(providerId, modelId)`（superRefine 校验）
- `properties`/`optionSpecs` 之外的键一律不许出现（strict）

## 5. 排错：静默降级与真正的日志

加载失败的**表现**：UI 里所有个人供应商消失（只剩内置 `account:*` 账户），但磁盘文件完好——纯加载层问题，不是数据丢失。

**CLI 日志（`~/.zcode/cli/log/`）不记录这个失败**；轮询错误被空 catch 吞掉。真正的报错在：

```bash
grep "provider-config" ~/.zcode/v2/logs/$(date +%Y-%m-%d).log
# → [provider-config] Personal Provider Config 加载失败，已保留磁盘状态并以内存空配置降级
#    {"error":{"name":"ZodError","message":"[{\"code\":\"unrecognized_keys\",…}]"}}
```

ZodError 带 `code` / `keys` / `path`，是**逐字段定位的唯一可靠来源**。修好后等一个轮询周期（≤60s），该日志不再新增「加载失败」即生效，无需重启客户端。

## 6. 三连坑实录（2026-10-02 实机故障）

| # | 错误 | 现象 | 定位手段 |
|---|---|---|---|
| 1 | 模型元数据只写 `{contextWindow, maxOutputTokens{max}}`，缺 `properties` 其余必填项 | UI 供应商全消失 | 逆向 zod 定义发现必填清单 |
| 2 | `maxOutputTokens` 照内置 site-rules 范例多写了 `map` 键 | 同上 | strict schema 的 pick 语义（模型级只有 `{max}`） |
| 3 | `modelConfigRules` 嵌进了 `providerConfigRules` **里面**（正确位置是 `config` 下平级） | 同上，且每 60s 报一次错 | **客户端主日志**的 `Unrecognized key: "modelConfigRules"` |

共同的教训：**JSON 合法 ≠ schema 合法**；客户端失败时静默降级不进 CLI 日志，必须去 `~/.zcode/v2/logs/` 看。

## 7. 模型清单同步流程（网关 ↔ ZCode）

1. `GET /v1/models` 与 `personalModelIds` 求差集，得新增/下线
2. 新模型先实测（anthropic 协议，非流式 + 流式各一次，小 `max_tokens`）
3. 追加进 `personalModelIds` + `modelOrder`
4. 同步模型元数据（§4），字段值取自网关 `/api/models`
5. 等轮询生效；改前备份，改后看 §5 的日志确认无「加载失败」

2026-10-02 以此流程接入过新模型 `space-bunny`。

## 8. 与 Agent2API 网关的结合点

- 供应商协议选 `anthropic-messages`，Base URL `http://127.0.0.1:8787/v1`（与 Claude Code 同链路）
- **网关需 2026-10-02 之后的构建**（≥ `4b33284`）：ZCode 的 system 模板沿用 Claude Code 的 gitStatus 指纹句式（`Main branch (you will usually use this for PRs)`），旧版脱敏词表未覆盖，上游会以 `Illegal API invocation from an unapproved channel` 整单拒绝——见 `internal/adapter/workbuddy/sanitize.go` 与 CHANGELOG
- schema 逆向来源：`/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs`（grep zod 定义：`Uj` / `OKe` / `hpe` / `DKe` / `M3i` / `Ipe`）
