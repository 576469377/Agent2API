# 客户端接入

[文档导航](README.md) · [API 参考](api.md) · [模型路由](configuration.md) · [故障排查](troubleshooting.md)

先完成[快速开始](quickstart.md)中的真实回复验证。客户端使用网关地址，默认 `http://127.0.0.1:8787`；独立控制台的 `8788` 用于管理。

## 地址与鉴权

| 协议 / 客户端 | 配置 |
|---|---|
| Chat Completions SDK | Base URL `http://127.0.0.1:8787/v1` |
| Responses HTTP | `POST http://127.0.0.1:8787/v1/responses` |
| Anthropic Messages HTTP | `POST http://127.0.0.1:8787/v1/messages` |
| Claude Code | `ANTHROPIC_BASE_URL=http://127.0.0.1:8787` |
| cc-switch | Anthropic，Base URL `http://127.0.0.1:8787`（不带 `/v1`） |
| ZCode 自定义供应商 | Anthropic Messages，Base URL `http://127.0.0.1:8787/v1` |

SDK 对 Base URL 是否自动追加 `/v1` 的行为可能不同；以最终请求打到上述端点为准，避免 `/v1/v1/messages` 或缺失 `/v1`。

- 网关未配置 `auth.api_key` 时不校验密钥；客户端要求必填可填 `agent2api-local`。
- 配置了网关密钥后，Chat / Responses 使用 `Authorization: Bearer <网关密钥>`，Anthropic Messages 使用 `x-api-key: <网关密钥>`。网关也接受 Bearer 形式的管理请求。
- 使用网关访问密钥，不要将 WorkBuddy 账号 token 填入客户端。
- 模型使用控制台复制的完整路由 ID；沿用客户端已有模型名时，可配置[模型别名](configuration.md#模型别名modelsaliases)。
- 客户端仅配置目录中**刚确认免费**的模型；付费、浮动费率和价格未知的条目不要导入。目录可见不代表账号有免费额度。

## ZCode 手动配置

在供应商设置中添加一个自定义供应商：

| 项目 | 值 |
|---|---|
| 名称 | Agent2API |
| 协议 | Anthropic Messages |
| Base URL | `http://127.0.0.1:8787/v1` |
| API Key | 网关密钥；未启用鉴权时可填 `agent2api-local` |
| 国内模型 | `workbuddy/模型名` |
| 国际模型 | `workbuddy-global/模型名` |
| Cursor Free 额度 | `cursor/composer-2.5` |
| OpenRouter 免费模型 | `openrouter/作者/模型名:free`（以目录完整 ID 为准） |

四个平台放在同一个供应商内，用模型前缀区分。OpenRouter 先按[接入说明](configuration.md#openrouter仅免费模型)配置自己的 API Key；客户端仍填写网关密钥。上下文窗口、输出上限与能力使用网关返回的元数据。

先在控制台“模型测试”验证所选免费路由。测试页不会使用同名裸 ID 自动选地区，也不会继续沿用已变为付费或价格未知的旧选择。完整目录可见不代表该模型应该加入客户端。

## ZCode 增量同步

需要 Python 3.9+、正在运行的网关和本地网关配置。无配置的网关可首次复制 `config.example.json` 为 `config.json`；有配置时使用实际启动所用的文件，确保密钥一致。

```powershell
# 先生成候选配置，不改动正在使用的 ZCode 配置
python scripts/sync-zcode.py --config config.json --dry-run

# 确认后应用
python scripts/sync-zcode.py --config config.json
```

脚本默认连接 `http://127.0.0.1:8787`，读写 `~/.zcode/v2/provider_config.json`。它会：

- 强制刷新上游目录，只同步费率在最近 5 分钟确认免费、且有上下文与输出上限的条目，包括 OpenRouter 和当前个人 Free 的 Cursor Composer 2.5。
- 删除本供应商原有的付费、浮动、价格未知、已下架条目及相关模型规则；保留免费条目的排序、启用状态、能力与推理设置，保留其他供应商。
- OpenRouter 的 ZCode 输出预算最多设为 8192 Token，上游公布的上限更小时使用较小值；已有过大的规则也会修正，保留用户设定的更小预算及完整上下文窗口。部分免费端点会拒绝目录自身标称的超大输出值，2026-10-19 已在 Dots 复现此问题。网关目录仍报告上游原始元数据。
- 一并迁移旧模型引用。免费默认模型保持原选择；默认模型被删除时，改选本供应商首个未停用的免费模型；若全停用则清除该默认选择。
- 配置内容相同时跳过写入和备份，保留原文件字节与修改时间。同步期间发现文件被 ZCode 或其他进程修改时停止，避免用读取目录前的旧配置覆盖新设置。
- 确有变更时，使用独立临时文件完整写入，再备份与原子替换；配置版本不支持或迁移出现重复设置时停止写入。这是重复写入与并发编辑保护，不代表已修复 ZCode 内部的所有重载问题。

`--dry-run` 生成的候选文件以及备份**含客户端密钥**，同样应留在本机，不要提交仓库。确认时可在本地编辑器查看供应商和模型信息，不要将完整配置贴到 Issue。

### 常用参数

| 参数 | 用途 |
|---|---|
| `--config` | 实际网关配置路径，读取其中的 API Key |
| `--base-url` | 网关 origin，例如 `http://127.0.0.1:8799`，不追加 `/v1` |
| `--zcode-config` | 自定义 ZCode 配置文件路径 |
| `--provider-id` / `--provider-name` | 内部 ID / 显示名称，默认 `agent2api` / `Agent2API` |
| `--models` | 只添加指定的免费模型；原有仍确认免费的模型保留，非免费条目仍会清理 |
| `--default-model` | 显式修改默认供应商与模型；该模型需在本次同步集合内 |
| `--dry-run` | 只写候选文件 |
| `--verify-usage` | 同步前后只读抽查本机 ZCode 历史统计，输出不含对话、模型及账号标识的汇总 |
| `--usage-db` | 为统计抽查指定本地数据库；默认从所选 `v2` 配置路径推导 `../cli/db/db.sqlite` |

显式选择非免费或缺少上下文 / 输出上限的条目时拒绝写入。目录刷新失败、费率过期或没有可确认的免费模型时停止写入，避免用旧目录误改配置。`--dry-run` 也会刷新目录，但不请求生成。

### 同步后 ZCode 的历史统计变空

ZCode「设置 → 使用统计 → 应用用量」与 Agent2API 网页控制台是两份独立统计。同步脚本只替换供应商配置，不删除会话、更新数据库或清理客户端缓存。2026-10-23 检查的 ZCode 3.16.2 从本地 Agent 数据库汇总历史，查询不依赖当前模型清单；已移除模型的历史条目仍可存在。该客户端的统计读取依赖已建立的工作区连接，页面没有自动重试；这是一条可能出现空白的路径，尚未证实是同步后空白的唯一原因。

另一台机器验证时，在更新仓库后运行：

```powershell
python scripts/sync-zcode.py --config config.json --verify-usage
```

`ZCode usage check:` 后的 JSON 对比同一截止时刻内的历史模型请求数、Token 总数及日期范围。`no_record_count_decrease` 仅表示两次抽查未观察到记录数减少，不是逐条校验，也不代表界面已经恢复；在途请求仍可能更新 Token 数。`record_count_decreased` 需要进一步排查，请保留原统计文件，不要用空库覆盖。`missing`、`unsupported_schema` 或 `unavailable` 表示未能读取该数据库，不能当作零用量。此检查只适用于当前机器的本地 ZCode Agent，不能验证远端工作区或其他 Agent 的统计。

检查使用 SQLite 只读模式并包含已提交的 WAL 记录，不创建缺失的数据库，不读取对话正文、原始用量 JSON 或凭证。只需提供这一行 JSON 与 ZCode 版本，不要提供完整配置或数据库。如果界面仍空白，可在「应用用量」点击刷新并核对时间范围；不要据此重建供应商或清理历史数据。

### 长会话等待与排错

客户端的等待上限需要覆盖实际上传和预处理。ZCode 会重新携带文字、工具历史及图片；一条很短的“继续”仍可能是很大的请求。网关默认建流预算为 600 秒，但客户端或前置代理可以更早结束等待。

没有首段输出时，先看用量分析中的上传 / 等待阶段和已用时；失败后按请求编号检查尝试记录、首段等待及实际路由。不要直接从超时推断封号或缓存失效。排查步骤见[长请求与超时](troubleshooting.md#长请求与超时)，指标定义见[口径手册](metrics.md)。

2026-10-18 本机 WorkBuddy 目录确认免费的路由为 `workbuddy/hy3`、`workbuddy-global/hy3`、`workbuddy-global/deepseek-v4.1-flash`、`workbuddy-global/step-5-preview`；这是一次观察，后续同步仍以实时目录为准。OpenRouter 的免费目录动态获取，不维护静态模型清单。

### 恢复

脚本在配置有变化时输出备份位置。需要恢复时，先保留当前文件，再将对应 `.bak-…` 备份复制回 `provider_config.json`。配置轮询间隔随版本变化：本轮检查的 3.16.2 默认为约 1 秒，早期版本曾约为 60 秒；是否加载成功可查看供应商列表及 `~/.zcode/v2/logs/`。这些备份仅恢复供应商配置，不恢复历史统计。旧版 schema 的调研记录见 [ZCode 调研](research/03-ZCode供应商配置逆向.md)。

## Claude Code

设置环境变量后启动客户端；模型别名需指向本人账号目录中的真实模型：

```powershell
# PowerShell
$env:ANTHROPIC_BASE_URL = 'http://127.0.0.1:8787'
$env:ANTHROPIC_API_KEY = 'agent2api-local'  # 启用鉴权时换成网关密钥
```

```bash
# macOS / Linux
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
export ANTHROPIC_API_KEY=agent2api-local
```

## cc-switch

cc-switch 是 Claude Code / Codex 等本地客户端的供应商配置管理器，多个网关或账号可随时切换。为 Agent2API 添加一条 Claude 供应商即可在它的界面里切换使用。

### UI 方式（推荐）

「添加供应商」时：

| 项 | 值 |
|---|---|
| 协议 / API 格式 | Anthropic（不是 OpenAI 兼容） |
| Base URL | `http://127.0.0.1:8787` |
| API Key | `agent2api-local`（启用网关鉴权后换成 `auth.api_key`） |
| 模型 | 从网关目录取完整路由 ID（如 `workbuddy/hy3`、`workbuddy-global/deepseek-v4.1-flash`） |

cc-switch 要求主模型必填，其余模型槽位（Subagent / Sonnet / Opus / Haiku / Fable）可全部填成同一个 ID——Agent2API 只按路由 ID 生效，不会因槽位不同而区别对待。只用一个模型时，填主模型即可。

### 直接改配置文件（未安装 app 或想批量导入）

cc-switch 把供应商存在本地 SQLite：`~/.cc-switch/cc-switch.db` 的 `providers` 表（`app_type='claude'` 为 Claude Code 可用条目）。**改前先退出 app**，否则内存状态会覆盖改动；改动后重启 app 生效。条目结构（`settings_config` 字段）：

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8787",
    "ANTHROPIC_AUTH_TOKEN": "agent2api-local",
    "ANTHROPIC_MODEL": "workbuddy-global/deepseek-v4.1-flash",
    "CLAUDE_CODE_SUBAGENT_MODEL": "workbuddy-global/deepseek-v4.1-flash",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "workbuddy-global/deepseek-v4.1-flash",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "workbuddy-global/deepseek-v4.1-flash",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "workbuddy-global/deepseek-v4.1-flash",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "workbuddy-global/deepseek-v4.1-flash"
  }
}
```

`meta` 字段填 `{"commonConfigEnabled": false, "apiFormat": "anthropic"}`。多条供应商只是模型 ID 不同，可按喜欢的顺序插入。

### 注意事项

- **Base URL 不带 `/v1`**：Claude Code / cc-switch 的 Anthropic 协议客户端会在后面自己拼 `/v1/messages`；带了会出现 `/v1/v1/messages`。
- **模型 ID 用完整路由 ID**（含平台前缀）。不带前缀时按网关的平台顺序解析，同名模型可能落到非预期平台。
- **国际版单请求图片上限**按模型不同，长会话可能触发 400，处理见[故障排查](troubleshooting.md)与[图片护栏配置](configuration.md#单请求图片数量护栏modelsimage_limits)。

## 先用 API 验证

[API 示例](api.md#请求示例)覆盖三种协议。模型先从目录获取，再用短消息验证。客户端报错时，优先查看控制台「调用日志」中的请求模型、实际模型、平台及原始错误。

客户端固定 system 模板可能触发上游渠道或关键词检查，建议保留网关默认的内容脱敏。若出现 `Illegal API invocation from an unapproved channel`，先确认使用当前代码与默认设置，见[故障排查](troubleshooting.md)。
