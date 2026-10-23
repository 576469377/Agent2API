# 平台支持

[文档导航](README.md) · [快速开始](quickstart.md) · [配置与模型路由](configuration.md)

以下按当前代码实现整理。三个生成端点共用平台适配器，但认证方式、模型能力和上游限制各不相同。模型目录表示支持范围，实际可用性仍取决于账号、额度与上游状态。

新增来源的现行免费权益、开源实现对照和后续接入顺序见 [2026-10-23 免费账号平台调研](research/06-免费账号平台接入调研.md)。其中 TRAE 国内版和 Kiro Free 已完成真实账号验证并接入。

## 接入方式

| 平台 ID | 凭证来源 | 启用方式 | 账号管理 |
|---|---|---|---|
| `workbuddy` | 国内桌面登录态或设备码授权 | 默认启用；单独使用 `-platform workbuddy` | 控制台添加 / 重新登录、独立号池、冷却与并发调度 |
| `workbuddy-global` | 国际桌面登录态或设备码授权 | 默认启用；单独使用 `-platform workbuddy-global` | 与国内版分开授权、存放凭证和调度 |
| `openrouter` | 自己的 API Key，可在控制台验证并保存 | 配置平台列表，或 `-platform openrouter` | 单密钥直连，可检测或替换连接 |
| `cursor` | 同一系统用户的 Cursor 桌面登录态 | 配置平台列表，或 `-platform cursor` | 只读复用桌面个人 Free 登录态；在 Cursor 中登录 |
| `trae-cn` | 官方网页授权，独立本机凭据 | 配置平台列表，或 `-platform trae-cn` | CLI / 控制台网页登录；单账号，不使用 WorkBuddy 号池 |
| `kiro` | 官方 Kiro CLI 登录态，不读取令牌 | 配置平台列表，或 `-platform kiro` | Google / GitHub / AWS Builder ID 官网授权、检测单账号 |

不带配置启动默认运行 WorkBuddy 两区；添加账号弹窗也会列出其他已实现平台的启用说明。[示例配置](../config.example.json)包含六个平台；首次复制后删除不使用的条目，并用 `-config config.json` 显式加载。配置平台列表后，`-platform` 不会替换该列表。OpenRouter、Cursor、TRAE 与 Kiro 不使用 WorkBuddy 的凭证目录或账号池控制操作。Kiro 的上下文、未知输出上限、Agent system 语义和工具续接限制见 [Kiro 指南](kiro.md)。

## 协议与能力

| 能力 | WorkBuddy 国内 / 国际 | OpenRouter 免费模型 | Cursor Free | TRAE 国内 Free | Kiro Free |
|---|---|---|---|---|---|
| Chat Completions / Responses / Messages | 均支持流式与非流式 | 均支持流式与非流式 | 均支持流式与非流式 | 均已实测流式与非流式 | 共用 ACP 适配；三种协议真实调用已验证 |
| 当前模型 | 按账号动态获取 | 价格明确为零的动态目录 | 仅 `composer-2.5` | DeepSeek-V4.1-Flash、GLM-5.3-Flash、Seed-2.1-Turbo | 已验证的五个账号可选模型，见 [Kiro](kiro.md) |
| 文本与历史 | 支持 | 支持 | 支持 | 支持 | 结构化历史传给原生 Agent |
| 图片输入 | 按模型目录；可配置数量护栏 | 按模型目录 | 暂不支持 | 暂不支持 | 暂不支持 |
| 工具调用与思考输出 | 按模型目录 | 按模型目录 | 支持；工具交还客户端执行 | 支持；三个模型均已验证工具续轮 | MCP 工具交回客户端，原生等待会话续接；不发布思考流 |
| system 指令 | 转换为上游消息 | 转发到官方 API | 作为客户端上下文，保留 Cursor 原生 Agent 规则 | 转换为原生 system 消息 | 作为原生 Agent 的结构化上下文 |
| 采样与生成选项 | 以模型和上游受理为准 | 以模型支持参数为准 | 不支持采样、effort、JSON 输出模式及 stop | 采样和思考控制不生效；拒绝图片、JSON 模式与 stop | 采样、预算与思考控制不生效；拒绝图片、JSON 模式及 stop |
| 输出上限 | 以模型目录为准 | 以模型目录及供应端限制为准 | 默认 / 最大 8192；网关按输出增量截断，含思考与工具步骤 | 上游忽略 max_tokens；目录值仅供参考，当前不截断 | 未公布；ZCode 8192 为建议预算，非上游限制 |

客户端优先复制目录中的完整 `routing_id`，例如 `workbuddy-global/模型名`、`openrouter/作者/模型:free`、`cursor/composer-2.5`。裸名按配置顺序路由；可选回退仅在建流前尝试其他平台的同名模型，不会自动寻找“相似模型”。见[模型路由](configuration.md#多平台配置upstreamplatforms)。

## 免费与额度的区别

- **WorkBuddy**：`x0.00` 表示目录中的零积分倍率，不代表无限额度。客户端接入、ZCode 同步和控制台模型测试只选择新近确认免费的条目；模型测试固定平台并在发送前复核。原始生成 API 仍允许手工请求账号可访问的其他模型，这不是全局免费拦截器。
- **OpenRouter**：适配器只发布和允许价格明确为零的模型，额外发送供应端零价格限制；目录过期且无法重新确认时拒绝生成。免费模型仍可能限流或额度耗尽。
- **Cursor**：每次生成检查个人 Free、未订阅、非团队、非学生及明确关闭按量付费；权益不完整或变化时拒绝调用。免费额度耗尽后报错，不自动转为付费。
- **TRAE 国内**：每次生成检查个人 Free、非试用、关闭按量计费且仅含已验证月赠与到账核对的签到免费积分；付费或未知来源拒绝。保留正积分倍率，控制台另标「免费额度」。详见 [TRAE 接入与限制](trae.md)。
- **Kiro**：每次新生成和工具结果续接均检查个人 Free 月度余额、关闭按量计费、无企业 / 追加积分 / 未验证赠金；只发布已验证且账号可选的明确模型。官方 CLI 管理登录态，账号页显示月度积分，见 [Kiro 指南](kiro.md)。

账号“就绪”、目录“支持”、费率“免费”和“仍有剩余额度”是不同信息。账号页按平台原始单位显示可查询的余额，不把积分换算为 Token、请求次数或金额。

## 下一步

| 想做什么 | 阅读 |
|---|---|
| 构建、选择平台并验证首次回复 | [快速开始](quickstart.md) |
| 配置 OpenRouter 密钥与免费目录 | [OpenRouter 配置](configuration.md#openrouter仅免费模型) |
| 配置 Cursor 路径、权益与参数 | [Cursor 配置](configuration.md#cursor个人-free-额度) |
| TRAE 官方网页登录、月赠积分与参数限制 | [TRAE 国内版](trae.md) |
| 登录 WorkBuddy、管理多账号 | [账号与调度](accounts.md) |
| 配置客户端与同步 ZCode | [客户端接入](clients.md) |
| 理解缓存、接收速度和估算 | [指标口径](metrics.md) |
| 升级、回退和保留统计 | [运行与升级](operations.md) |
| 理解已验证范围和协议依据 | [WorkBuddy 国际版调研](research/04-WorkBuddy国际版接入.md) · [Cursor 接入验证](research/05-Cursor接入调研.md) |

WorkBuddy 国际桌面加密凭证目前仅在 Windows 完成端到端验证。Cursor 在 Windows 使用系统 SQLite 读取登录态，macOS / Linux 的读取路径需要 Python 3；可构建不等于各平台均已完成实机验证。
