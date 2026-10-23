# Kiro Free

[平台支持](platforms.md) · [账号管理](accounts.md) · [客户端接入](clients.md) · [免费平台调研](research/06-免费账号平台接入调研.md)

2026-10-23 已在 Windows 原生官方 Kiro CLI **2.33.1**、Google 登录的个人 Free 账号上验证：明确模型选择、流式文本、调用方工具注册和工具结果续接。使用官方 ACP / MCP 接口；凭证由 CLI 管理，网关不读取或导出原生登录令牌。[官方 ACP](https://kiro.dev/docs/cli/acp/)

## 免费条件与模型

官方 Free 为每月 **50 credits**，不是 50 次请求，也不是免费无限量；每个模型保留官方真实积分倍率。每次新生成、每次工具结果续接前检查个人 `KIRO FREE`、关闭按量计费、非企业、无追加积分及未验证赠金、月度余额大于零。无法确认即拒绝；不自动购买、升级或切换付费账号。[官方定价](https://kiro.dev/pricing/) · [账单说明](https://kiro.dev/docs/billing/)

当前验证账号目录如下。以账号实时目录与 ACP 会话目录的交集为准，不把官网宣传范围或其他账号权益直接加入客户端。

| 完整路由 | CLI 公布的上下文 Token | 积分倍率 |
|---|---:|---:|
| `kiro/deepseek-3.2` | 164,000 | 0.25 |
| `kiro/minimax-m2.5` | 196,000 | 0.25 |
| `kiro/minimax-m2.1` | 196,000 | 0.15 |
| `kiro/glm-5` | 200,000 | 0.5 |
| `kiro/qwen3-coder-next` | 256,000 | 0.05 |

官网也列出部分 Free 账号的 Sonnet 4.5，但当前验证账号未提供，因此不发布。Auto、未知模型和付费模型不透传。上述窗口来自账号元数据，尚未进行接近上限的长输入实测；不同平台同名模型的窗口可能不同。[官方模型范围](https://kiro.dev/docs/models/available-models/)

## 登录与启用

1. 在运行网关的电脑上[安装官方 Kiro CLI](https://kiro.dev/docs/getting-started/installation/)。支持从 PATH、Windows 官方默认位置或 `cli_path` 查找；当前只验证了上面的 CLI 版本。
2. 将以下条目追加到实际启动配置的 `upstream.platforms` 列表，保留已有平台，然后启动网关：

   ```json
   { "id": "kiro", "model_cache_ttl_sec": 60 }
   ```

   自定义 CLI 位置时增加 `"cli_path": "C:/Tools/Kiro/kiro-cli.exe"`。Kiro 不使用 `base_url`、API Key、WorkBuddy 凭证目录或账号池。
3. 控制台选择「添加账号 → Kiro Free」，选择 Google、GitHub 或 AWS Builder ID，点「开始登录」。官方 CLI 会请求系统浏览器打开授权页；弹窗显示设备码，CLI 返回链接时也会显示链接。授权最长等待 30 分钟，完成后检测连接并更新免费目录。
4. 已在同一系统用户下登录 CLI 的账号，直接点「检测现有连接」，不必重复授权。

也可以使用命令行：

```powershell
agent2api login -platform kiro -provider google
agent2api -config config.json
```

`-provider` 支持 `google`、`github`、`builderid`；可用 `-cli-path` 指定登录所用 CLI。之后网关的 `cli_path` 应指向相同安装。重新登录会更新官方 CLI 的当前单账号，暂不提供多账号轮询。

## ZCode 与能力限制

控制台「客户端接入 → 一键配置 ZCode」会核验 Free 权益、刷新目录并同步 `kiro/` 路由。Python 增量同步脚本采用同样筛选规则。保留原供应商、默认选择、已保留模型的停用状态和更小预算，写入前备份。

- CLI **没有公布最大输出 Token**，网关目录用 `max_output_tokens: 0` 表示未知。ZCode 首次同步采用 **8192 的建议客户端预算**，不是上游保证或实测输出上限；已有更小预算保留。
- ACP 不提供严格的 `max_tokens`、采样、seed、思考强度或提示缓存控制，网关不承诺这些参数生效。ZCode 的思考选项固定关闭且不注入思考参数。图片、JSON 模式、`stop` 明确拒绝。
- 当前 ACP 返回积分用量但不返回输入 / 输出 Token；不根据积分虚构 Token 或缓存用量。控制台额度区显示 CLI 核验的月度积分，缺失 Token 依现有统计口径保持未知。
- 这是官方 Agent 的协议桥接。system 与会话历史以结构化上下文交给原生 Agent，不等同于直接访问基础模型的原生 system 指令。
- 工具通过独立、仅本机的 MCP 桥交给客户端执行。专用 Agent 只启用调用方工具，不启用本机文件、终端、仓库扫描、全局 MCP 或 Powers。工具返回续接原生等待中的请求，不另发一次付费式生成。工具结果包含错误标记时原样回传。
- 工具等待会话保存在当前网关进程内，受 `stream_total_timeout_sec` 限制。网关重启、超时或中途取消后需要重开请求；不能跨机器恢复。普通新对话轮次按客户端完整历史建立新原生会话。
- 官方 CLI 可能保存自己的会话和诊断记录。桥接工作目录位于 `~/.agent2api/kiro-bridge/`，结束时清理；不要将 CLI 私有缓存或原始调试记录提交仓库。

`kiro_cli_missing` 表示 CLI 未安装或路径失效；`kiro_free_only` 表示套餐或积分来源未通过；`kiro_free_credits_exhausted` 表示月度额度耗尽；`kiro_profile_unavailable` 表示 CLI 未应用专用工具配置，网关会停止调用。先在添加账号弹窗检测连接，再重新同步客户端。
