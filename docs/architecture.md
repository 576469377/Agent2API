# 当前实现架构

[文档导航](README.md) · [贡献指南](../CONTRIBUTING.md)

Agent2API 的核心是多平台协议网关。WorkBuddy 是目前落地的适配器，国内版与国际版是两个独立运行时；平台接入不应写入下游协议层。

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

## 新增平台

1. 在 `internal/adapter/` 下实现该平台的 `Adapter`，将私有请求、SSE 和错误转换到 `internal/llm`。
2. 在启动装配层注册平台、默认地址和凭证来源，为该平台建立独立运行时；支持设备登录时同步实现对应登录流程。
3. 通过 Hub 发布模型目录及平台归属。客户端使用 `平台ID/模型ID` 选择来源，重名裸模型按配置顺序处理。
4. 控制台从管理 API 读取平台和账号数据；补充名称及平台特有操作，避免将凭证格式假设推广到所有平台。
5. 用离线样本验证协议、鉴权、目录和路由，并更新平台支持说明。上游工具、图片和思考能力应以实际实现为准。

Chat Completions、Responses 与 Messages 的通用编解码通常可以直接复用。当前设备登录与加密桌面凭证实现针对 WorkBuddy，未来平台需要自己的认证逻辑；可扩展架构不表示其他平台已经可以调用。
