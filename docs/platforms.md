# 平台支持

[文档导航](README.md) · [快速开始](quickstart.md) · [配置与模型路由](configuration.md)

以下按当前代码实现整理。三个生成端点共用平台适配器，但认证方式、模型能力和上游限制各不相同。模型目录表示支持范围，实际可用性仍取决于账号、额度与上游状态。

## 接入方式

| 平台 ID | 凭证来源 | 启用方式 | 账号管理 |
|---|---|---|---|
| `workbuddy` | 国内桌面登录态或设备码授权 | 默认启用；单独使用 `-platform workbuddy` | 控制台添加 / 重新登录、独立号池、冷却与并发调度 |
| `workbuddy-global` | 国际桌面登录态或设备码授权 | 默认启用；单独使用 `-platform workbuddy-global` | 与国内版分开授权、存放凭证和调度 |
| `openrouter` | 自己的 API Key，推荐 `OPENROUTER_API_KEY` | 配置平台列表，或 `-platform openrouter` | 单密钥直连；在启动环境 / 配置中管理密钥 |
| `cursor` | 同一系统用户的 Cursor 桌面登录态 | 配置平台列表，或 `-platform cursor` | 只读复用桌面个人 Free 登录态；在 Cursor 中登录 |

不带配置启动仅保留 WorkBuddy 两区的登录入口。[示例配置](../config.example.json)包含四个平台；首次复制后删除不使用的条目，并用 `-config config.json` 显式加载。配置平台列表后，`-platform` 不会替换该列表。OpenRouter 与 Cursor 不使用 WorkBuddy 的设备登录、凭证目录或账号池控制操作。

## 协议与能力

| 能力 | WorkBuddy 国内 / 国际 | OpenRouter 免费模型 | Cursor Free |
|---|---|---|---|
| Chat Completions / Responses / Messages | 均支持流式与非流式 | 均支持流式与非流式 | 均支持流式与非流式 |
| 当前模型 | 按账号动态获取 | 价格明确为零的动态目录 | 仅 `composer-2.5` |
| 文本与历史 | 支持 | 支持 | 支持 |
| 图片输入 | 按模型目录；可配置数量护栏 | 按模型目录 | 暂不支持 |
| 工具调用与思考输出 | 按模型目录 | 按模型目录 | 支持；工具交还客户端执行 |
| system 指令 | 转换为上游消息 | 转发到官方 API | 作为客户端上下文，保留 Cursor 原生 Agent 规则 |
| 采样与生成选项 | 以模型和上游受理为准 | 以模型支持参数为准 | 不支持采样、effort、JSON 输出模式及 stop |
| 输出上限 | 以模型目录为准 | 以模型目录及供应端限制为准 | 默认 / 最大 8192；网关按输出增量截断，含思考与工具步骤 |

客户端优先复制目录中的完整 `routing_id`，例如 `workbuddy-global/模型名`、`openrouter/作者/模型:free`、`cursor/composer-2.5`。裸名按配置顺序路由；可选回退仅在建流前尝试其他平台的同名模型，不会自动寻找“相似模型”。见[模型路由](configuration.md#多平台配置upstreamplatforms)。

## 免费与额度的区别

- **WorkBuddy**：`x0.00` 表示目录中的零积分倍率，不代表无限额度。客户端接入、ZCode 同步和控制台模型测试只选择新近确认免费的条目；模型测试固定平台并在发送前复核。原始生成 API 仍允许手工请求账号可访问的其他模型，这不是全局免费拦截器。
- **OpenRouter**：适配器只发布和允许价格明确为零的模型，额外发送供应端零价格限制；目录过期且无法重新确认时拒绝生成。免费模型仍可能限流或额度耗尽。
- **Cursor**：每次生成检查个人 Free、未订阅、非团队、非学生及明确关闭按量付费；权益不完整或变化时拒绝调用。免费额度耗尽后报错，不自动转为付费。

账号“就绪”、目录“支持”、费率“免费”和“仍有剩余额度”是不同信息。当前未实现统一余额查询或金额估算。

## 下一步

| 想做什么 | 阅读 |
|---|---|
| 构建、选择平台并验证首次回复 | [快速开始](quickstart.md) |
| 配置 OpenRouter 密钥与免费目录 | [OpenRouter 配置](configuration.md#openrouter仅免费模型) |
| 配置 Cursor 路径、权益与参数 | [Cursor 配置](configuration.md#cursor个人-free-额度) |
| 登录 WorkBuddy、管理多账号 | [账号与调度](accounts.md) |
| 配置客户端与同步 ZCode | [客户端接入](clients.md) |
| 理解缓存、接收速度和估算 | [指标口径](metrics.md) |
| 升级、回退和保留统计 | [运行与升级](operations.md) |
| 理解已验证范围和协议依据 | [WorkBuddy 国际版调研](research/04-WorkBuddy国际版接入.md) · [Cursor 接入验证](research/05-Cursor接入调研.md) |

WorkBuddy 国际桌面加密凭证目前仅在 Windows 完成端到端验证。Cursor 在 Windows 使用系统 SQLite 读取登录态，macOS / Linux 的读取路径需要 Python 3；可构建不等于各平台均已完成实机验证。
