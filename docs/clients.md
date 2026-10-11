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
| ZCode 自定义供应商 | Anthropic Messages，Base URL `http://127.0.0.1:8787/v1` |

SDK 对 Base URL 是否自动追加 `/v1` 的行为可能不同；以最终请求打到上述端点为准，避免 `/v1/v1/messages` 或缺失 `/v1`。

- 网关未配置 `auth.api_key` 时不校验密钥；客户端要求必填可填 `agent2api-local`。
- 配置了网关密钥后，Chat / Responses 使用 `Authorization: Bearer <网关密钥>`，Anthropic Messages 使用 `x-api-key: <网关密钥>`。网关也接受 Bearer 形式的管理请求。
- 使用网关访问密钥，不要将 WorkBuddy 账号 token 填入客户端。
- 模型使用控制台复制的完整路由 ID；沿用客户端已有模型名时，可配置[模型别名](configuration.md#模型别名modelsaliases)。

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

两个区域放在同一个供应商内，用模型前缀区分。上下文窗口、输出上限与能力使用网关返回的元数据，不要由模式入口的名字猜测其底层模型。

## ZCode 增量同步

需要 Python 3.9+、正在运行的网关和本地网关配置。无配置的网关可首次复制 `config.example.json` 为 `config.json`；有配置时使用实际启动所用的文件，确保密钥一致。

```powershell
# 先生成候选配置，不改动正在使用的 ZCode 配置
python scripts/sync-zcode.py --config config.json --dry-run

# 确认后应用
python scripts/sync-zcode.py --config config.json
```

脚本默认连接 `http://127.0.0.1:8787`，读写 `~/.zcode/v2/provider_config.json`。它会：

- 将两个区域的模型统一为 `平台/模型` ID，补充有上下文窗口和输出上限的条目。
- 保留原有模型、排序、启用状态、能力与推理设置，以及其他供应商。
- 一并迁移旧模型引用和默认选择，不主动切换默认供应商或模型。
- 应用前备份原文件，随后原子替换；配置版本不支持或迁移出现重复设置时停止写入。

`--dry-run` 生成的候选文件以及备份**含客户端密钥**，同样应留在本机，不要提交仓库。确认时可在本地编辑器查看供应商和模型信息，不要将完整配置贴到 Issue。

### 常用参数

| 参数 | 用途 |
|---|---|
| `--config` | 实际网关配置路径，读取其中的 API Key |
| `--base-url` | 网关 origin，例如 `http://127.0.0.1:8799`，不追加 `/v1` |
| `--zcode-config` | 自定义 ZCode 配置文件路径 |
| `--provider-id` / `--provider-name` | 内部 ID / 显示名称，默认 `agent2api` / `Agent2API` |
| `--models` | 只同步指定模型，已有模型仍保留 |
| `--default-model` | 显式修改默认供应商与模型；该模型需在本次同步集合内 |
| `--dry-run` | 只写候选文件 |

默认跳过缺失上下文或输出上限的条目；显式选择这类条目时拒绝写入。不会自动删除旧模型：历史条目失效时，可在客户端中单独停用或删除。

### 恢复

脚本输出会指出备份位置。需要恢复时，先保留当前文件，再将对应 `.bak-…` 备份复制回 `provider_config.json`。当前已验证的 ZCode 客户端约每 60 秒重新读取配置；是否加载成功可查看供应商列表及 `~/.zcode/v2/logs/`。版本差异和严格 schema 的细节见[ZCode 调研](research/03-ZCode供应商配置逆向.md)。

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

## 先用 API 验证

[API 示例](api.md#请求示例)覆盖三种协议。模型先从目录获取，再用短消息验证。客户端报错时，优先查看控制台「调用日志」中的请求模型、实际模型、平台及原始错误。

客户端固定 system 模板可能触发上游渠道或关键词检查，建议保留网关默认的内容脱敏。若出现 `Illegal API invocation from an unapproved channel`，先确认使用当前代码与默认设置，见[故障排查](troubleshooting.md)。
