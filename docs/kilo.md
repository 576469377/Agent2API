# Kilo 官方匿名免费通道

[平台支持](platforms.md) · [客户端配置](clients.md) · [调研记录](research/07-更充足的免费模型来源调研.md)

Kilo 提供无需账号、充值、信用卡或 API Key 的官方免费模型入口。Agent2API 的 `kilo` 适配器只使用该匿名权限，支持 Chat Completions、Responses、Anthropic Messages、流式及非流式输出和客户端执行的工具。[官方认证说明](https://kilo.ai/docs/gateway/authentication)

## 启用和添加连接

单独运行：

```powershell
go build -trimpath -o bin/agent2api.exe ./cmd/agent2api
./bin/agent2api.exe -platform kilo
```

在已有多平台配置的 `upstream.platforms` 中追加以下条目，保留原有平台并重新启动网关：

```json
{"id": "kilo", "model_cache_ttl_sec": 60}
```

打开控制台「添加账号 → Kilo Free → 检测免费通道」。该按钮读取当前免费目录，不生成回复、不创建 WorkBuddy 凭证目录。账号卡显示匿名连接，没有登录态、密钥保存或号池操作。未启用的平台也会保留添加入口及配置说明。

不需要填写 `KILO_API_KEY`。适配器不读取此变量，也不向上游发送本地网关密钥、Kilo 账号令牌、组织标识或 BYOK 凭据。请求不跟随重定向。

## 免费模型筛选

模型必须同时满足以下条件才进入目录和三个客户端的一键配置：

- 公共目录明确提供 `isFree: true`，且输入、输出及其他所有价格字段都为明确的零值。
- 是可以直接指定的模型；不发布 Auto、Router 或重复且有歧义的 ID。
- 声明文本输出、工具调用、上下文窗口和最大输出预算。音乐、审核分类或能力不明的条目不导入。
- 免费活动尚未到期；日期无效时拒绝发布。仅提供日期而没有时区时，保守地在该日 UTC 零点下架。

目录展示缓存最长一分钟，每次生成及工具结果续轮均重新核验当前目录；读取失败时不使用旧价格继续生成。即使上游价格在核验后变化，请求仍然不带账号凭据，不能使用已充值余额、组织预算或账号 BYOK。Kilo 将匿名权限限制为免费模型。[模型目录与供应商](https://kilo.ai/docs/gateway/models-and-providers)

模型名称不一定带 `:free`，例如 `reflection/beam-free`，不能只按名称后缀判断价格。上下文与输出上限按托管端点目录保留，不统一改为 200K，也不把输入和输出窗口相加。

## 模型路由和一键配置

在模型目录复制完整的 `kilo/作者/模型` 路由，例如 `kilo/reflection/beam-free`。明确的平台前缀防止同名模型选择到其他来源。三个下游端点使用相同路由；上游为 `https://ai-gateway.kilo.ai/api/v1`。[官方接口](https://kilo.ai/docs/gateway/api-reference)

控制台「客户端接入」提供 ZCode、cc-switch 和 pi 按钮。cc-switch 可选择 Claude Code、Codex 或两者；pi 将模型放在 `agent2api-kilo` 供应商中。写入时再次核验免费目录，保留既有备份、并发修改检测和重复同步不重写的行为。客户端连接本地网关，不保存上游 Kilo 密钥。

Responses 保留 MCP 的函数命名空间、原始工具名称及 `call_id`。模型偶尔返回 `namespace.内部别名` 时，仅恢复本次请求中已经登记的同一配对；不会按相似名称猜测工具。工具由调用客户端执行，本适配器不执行本地命令。

## 限流和可用性

官方文档规定，免费请求按出口 IP 共享每小时 200 次，登录不会增加同一 IP 的容量。工具续轮也会产生新的模型请求；一个编码任务通常包含多次请求。[免费请求限制](https://kilo.ai/docs/gateway/usage-and-billing)

这不是对本机剩余次数的承诺。本轮在达到该标称次数之前收到匿名请求受限响应；共享出口的其他用量或额外限制无法从公共接口确认。网关保留匿名受限状态，不根据标称值伪造剩余次数或恢复时间。

模型供应商还可能独立限流或暂时耗尽资源。网关识别明确的 IP 限制；供应商返回 `upstream_provider_shared_pool` 时保留模型供应商范围，不把它解释为整个 Kilo 免费额度耗尽。遵循上游 `Retry-After`；不使用付费回退或 BYOK 来绕开限制。

目录免费、工具能力声明和当前调用成功是不同证据。模型可能仍返回 404、429、502 或超时。账号卡没有可查询的匿名请求剩余额度，不显示虚构余额；模型详情保留上游描述及提示留存 / 训练标记。

本轮验收使用独立服务、客户端配置副本及合成提示；普通文本与工具结果均使用随机标记，工具结果在收到调用后才生成，避免把模型猜测当作工具执行。[逐模型验收](reviews/2026-10-24-kilo-acceptance.md)区分通过、供应商故障和匿名请求受限，不将整个目录标记为全部可用。
