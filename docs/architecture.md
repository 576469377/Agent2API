# 当前实现架构

[文档导航](README.md) · [贡献指南](../CONTRIBUTING.md)

```
internal/
├── llm/          IR 层（零依赖）：请求/响应/错误的中间表示
├── adapter/      上游平台接缝：Adapter 接口
│   └── workbuddy/  WorkBuddy 实现：认证 / 请求 / SSE / 模型 / 脱敏
├── api/          下游协议编解码
│   ├── common/     SSE 写出、错误载荷（三协议共用）
│   ├── openai/chat/ · openai/responses/ · anthropic/messages/
├── app/          HTTP 路由与编排（hub：多平台运行时）
├── obs/          指标采集与持久化
├── web/          内置控制台（go:embed）
└── config/       配置
```

**设计要点**：`api/*` 与 `adapter/*` 互不 import，只通过 `internal/llm` 通信 —— 把「N 个平台 × M 个协议」的复杂度从 N×M 降为 N+M。接入新平台时，三个下游协议一行都不用改。

多平台编排由 [`internal/app/hub.go`](../internal/app/hub.go) 的 **Hub** 承担：启动时为每个平台建独立的适配器/号池/登录会话，构建「模型 ID → 平台」索引，请求按模型名路由、未命中透传默认平台。号池（`Pool`）对 Hub 来说就是一个普通 Adapter，多账号与多平台两层互不感知。

接口刻意做小：`Adapter` 只有 3 个方法（`Stream` / `ListModels` / `Name`），`ResponseStream` 只有 1 个（`Recv`）。造假适配器做测试只要十行。

延伸阅读：

- 最初设计（历史稿）—— [`docs/design/01-架构设计.md`](design/01-架构设计.md)
- 上游协议逆向 —— [`docs/research/02-WorkBuddy上游协议逆向.md`](research/02-WorkBuddy上游协议逆向.md)

国内版与国际版复用 WorkBuddy 协议实现，但分别拥有凭证、目录、号池与登录会话。`internal/app/routing.go` 在建流前处理可选同名回退；流开始后不会切换平台。

`cmd/agent2api` 运行网关及嵌入式控制台；`cmd/console` 从磁盘读取静态资源，并将 API / SSE 转发给正在运行的网关，适合更新前端而保持原网关在线。
