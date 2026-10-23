# Agent2API 文档

[项目首页](../README.md) · [English overview](../README.en.md) · [更新日志](../CHANGELOG.md)

按你现在要完成的任务选择入口。首次使用先拿到一条真实免费模型回复；排错时先看单次记录和有效配置，不需要从协议研究稿开始读。

## 我现在要做什么

| 目标 | 直接阅读 |
|---|---|
| 第一次安装、登录和启动 | [快速开始](quickstart.md) |
| 了解六个平台和免费边界 | [平台支持](platforms.md) |
| 接入 ZCode / Claude Code / 其他客户端 | [客户端接入](clients.md) |
| 调用失败、很慢或一直没有输出 | [故障排查](troubleshooting.md) |
| 升级网关且保留统计与账号 | [运行、升级与恢复](operations.md) |
| 分清 Token、缓存、P95、实时速度和历史估算 | [指标与数据口径](metrics.md) |
| 修改代码或了解仓库 | [开发与仓库导览](development.md) |

## 开始使用

1. 选一种自己已有授权的平台；不需要所有平台同时可用。
2. 按[快速开始](quickstart.md)构建网关，在模型测试中验证真实回复。健康检查只验证本地服务。
3. 按[客户端接入](clients.md)配置已确认免费的完整模型路由，例如 `workbuddy-global/模型名`。
4. 遇到问题，从调用日志进入详情，核对平台、错误码、尝试和等待时间。

| 指南 | 解决的问题 |
|---|---|
| [平台支持](platforms.md) | 认证来源、协议能力、免费与额度的区别 |
| [快速开始](quickstart.md) | Windows / macOS / Linux 的构建、登录和首次验证 |
| [客户端接入](clients.md) | Base URL、网关密钥、ZCode 免费配置与备份 |

## 日常操作

| 指南 | 解决的问题 |
|---|---|
| [控制台](console.md) | 九个页面、日志筛选、详情、实时状态与独立预览 |
| [Kiro Free](kiro.md) | 官方 CLI 网页登录、自选免费模型、客户端工具与 ZCode 同步 |
| [账号与调度](accounts.md) | WorkBuddy 两区登录、亲和、并发、限流与重新授权 |
| [运行、升级与恢复](operations.md) | 进程与源码的区别、空闲升级、回退、备份和旧存档 |
| [故障排查](troubleshooting.md) | 从具体错误定位问题，保留可分享的诊断摘要 |
| [请求结构诊断](request-diagnostics.md) | 不记录密钥或正文的 ZCode 参数与图片历史摘要 |

## 参考与解释

| 文档 | 范围 |
|---|---|
| [配置与模型路由](configuration.md) | CLI / 环境变量 / JSON、字段默认值、模型限定名、别名和预算 |
| [API 参考](api.md) | 生成与管理端点、鉴权、响应头、错误及三协议示例 |
| [指标与数据口径](metrics.md) | 累计 / 日期 / 样本 / 实时范围、缓存分母、缺失值和估算 |
| [当前实现架构](architecture.md) | 请求路径、模块边界、区域隔离和流的生命周期 |
| [开发与仓库导览](development.md) | 修改入口、应保持的不变量和对应测试 |

## 维护与贡献

| 文档 | 范围 |
|---|---|
| [贡献指南](../CONTRIBUTING.md) | 构建、格式、测试、文档检查和提交约定 |
| [离线样本](../fixtures/README.md) | 上游金帧回放、IR 断言与脱敏要求 |
| [安全策略](../SECURITY.md) | 本地凭证、访问边界与漏洞报告 |
| [免责声明](../DISCLAIMER.md) | 项目用途及使用边界 |
| [发布检查](开源发布检查清单.md) | 维护者的发布参考 |

## 调研与历史记录

[最新深入调研](reviews/2026-10-24-deep-runtime-review.md)对照认证、网关调度、指标和成熟控制台源码，记录并发刷新、冷却唤醒、统计恢复与浏览器旧响应问题，附可复现基准。[目录与客户端审查](reviews/2026-10-24-catalog-and-client-review.md)记录跨平台刷新隔离、缓存失效、客户端同步竞争和连接收尾。[前一轮项目优化审查](reviews/2026-10-24-project-optimization-review.md)记录协议兼容、阻塞取消、指标与前端性能、静态缓存、跨平台验证及外部源码对照。[控制台与文档审查](reviews/2026-10-23-console-and-repository-review.md)记录设计与文档调整；[网关稳定性审查](reviews/2026-10-23-gateway-stability-review.md)记录请求预算、队列和大请求超时修正。

- [调研索引](research/README.md)：协议、ZCode、WorkBuddy 国际版和 Cursor 的证据与日期边界。
- [设计索引](design/README.md)：历史方案、视觉规范和当时的取舍。
- [2026-10-12 核心逻辑审查](reviews/2026-10-12-proxy-logic-review.md)、[控制台复查](reviews/2026-10-12-console-review.md)：保留历史问题与修复过程。

历史稿中的版本号、模型数、后续计划和实测结果只适用于记录时的环境。当前行为优先依据本页链接的使用指南、有效配置和代码；可构建不代表每个平台都已完成实机验证。
