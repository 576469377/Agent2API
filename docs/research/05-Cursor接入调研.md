# Cursor 接入验证记录

验证日期：2026-10-18。已实现个人 Free 账号的 Composer 2.5 反代，支持三种下游协议。配置步骤与兼容性限制见[配置文档](../configuration.md#cursor个人-free-额度)。

## 客户端升级与认证

原安装为 3.0.9。按用户要求，从 [Cursor 官方下载页](https://cursor.com/download)取得 Windows x64 User 安装包并升级到 **3.24.18**；安装包和安装后的主程序均通过 Anysphere, Inc. Authenticode 签名验证。安装成功，不需重启系统，原登录态保留。

服务器 GetServerConfig 对当前实际版本返回 CLIENT_UPDATE_LEVEL_NONE，最低支持版本为 3.12.0。只提高版本字段不足以修复生成：桌面认证还需要该安装已有的设备标识生成 x-cursor-checksum。实现读取实际 package / product 与 storage.json，不伪造新版本、不生成或重置设备 ID。

令牌从 User/globalStorage/state.vscdb 只读读取，仅在内存中使用。Windows 使用系统 winsqlite3.dll，无 CGO、外部数据库依赖或凭证导出；其他系统使用 Python 3 的只读 SQLite。桌面令牌与官方 [SDK 用户 API Key](https://cursor.com/docs/sdk/typescript)不同；当前适配器使用桌面路径，不要求用户另建 SDK Key。

## 免费权益与发布范围

真实接口返回个人 Free、individual Free、订阅 unpaid、非团队、非学生，GetHardLimit.noUsageBasedAllowed: true。目录可见 30 多个条目，但没有明确的价格字段，因此不将整份目录推断为免费模型。

适配器仅允许本次实际验证的 composer-2.5，且每次生成前重新读取凭证、免费权益与禁止按量付费状态，再确认模型仍在账号目录中。付费、团队、试用、计费状态缺失或不符均拒绝生成；Auto、Max、Fast 与其他未验证模型均拒绝调用。目录的 cursor_free_plan 表示当前个人 Free 限额权益，不表示永久零单价或无限量。官方 [套餐说明](https://cursor.com/docs/models-and-pricing)也将 Free 定义为有限使用量。

## 已通过的真实调用

- 当前账号 Composer 2.5 返回 OK，收到 turnEnded 和输入 / 输出 / 缓存用量。
- 历史消息中的测试 token 能在后续回答中被回忆。
- Go 网关的 Chat Completions、Responses 非流式调用均返回真实文本；Anthropic Messages 流式调用返回文本与 message_stop。
- Anthropic 工具调用生成正确工具名和参数；调用方传回固定测试工具结果后，模型使用该结果继续回答。网关没有执行工具或访问本地工作区。

生成端使用 GetServerConfig.agentUrlConfig.agentUrl 返回的受信任 Cursor 域名，通过 HTTP/2 调用 agent.v1.AgentService/Run。请求和响应是 Connect JSON 帧；KV Blob 仅存在当前请求内存中。模型工具定义和查询只限本次客户端声明的工具，工具执行请求转换为标准工具调用交还客户端。

必须看到 turnEnded 才将普通回复记为成功；Connect 错误、提前断流、无结束帧、超时与交互确认均按失败处理。客户端工具调用以 tool_calls 结束当前 API 响应，取消上游等待，下一次由客户端提供历史和结果。

## 兼容性边界

Cursor 是 Agent 服务，保留原生系统规则。实测旧 requestContext Rules 及自建 root system 消息不能提供标准 API 的 system 覆盖语义；适配器将客户端 system 明确标注为上下文转交。该文本可在上游会话中确认收到，但模型仍可能优先遵循 Cursor 自身规则。图片与未验证的模型能力不对外宣称支持。

2026-10-19 根据本机 ZCode 使用记录修复工具续接：旧实现把历史调用与结果拼成文本，曾出现普通文本中的伪工具调用及提前结束。新版按 3.24.18 本地运行时的 CoreMessage 格式传递 `tool-call` / `tool-result`，保留调用 ID、MCP 工具名、参数、结果与错误状态；同时完整提供 `mcpMetaToolOptions.mcpDescriptors`，关闭工具描述文件的发现模式。原生 Read 在本次客户端声明同名兼容 `file_path` / 行范围 schema 时转回客户端执行，不访问宿主文件。实测使用 ZCode 的 35 个工具定义和合成数据，连续完成两次 Bash、两次 Read 后正确汇总产品名与版本，无伪工具调用；生成测试没有执行模型返回的命令或读取测试路径。

采样、effort、JSON 模式及 stop 参数未开放，明确拒绝。输出预算通过上游 token 增量本地截断，包含思考和工具步骤，并非原生生成参数。工具提前返回时缺少最终输入结算，用量仅报告已观察输出；普通完整回复采用 turnEnded 结算。

旧 aiserver.v1.AiService/StreamChat 已停用，会先发升级提示再返回 ERROR_DEPRECATED，不能当作生成成功。旧版、缺少设备标识或不完整配置曾返回 ERROR_OUTDATED_CLIENT。目前不使用这些失败路径作为生成实现。

## 可重复的只读检查

    python scripts/diagnose-cursor.py

脚本只读本地登录态、账号状态、版本要求与目录，不生成、不修改账号或数据库，也不输出令牌、邮箱或支付标识。它本身不验证生成，因此输出的 generation_verified 仍是 false，confirmed_free_generation_models 仍为空；不要将只读诊断结果与上面的真实调用记录混为一谈。
