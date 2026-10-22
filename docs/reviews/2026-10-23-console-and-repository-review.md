# 控制台、文档与全仓审查

[文档导航](../README.md) · [开发导览](../development.md) · [前一轮稳定性审查](2026-10-23-gateway-stability-review.md)

审查日期：2026-10-23。基线为 `9a4a27a`。本轮检查 `cmd/`、`internal/` 全部包的分工与关键路径，交叉阅读测试、脚本、CI、配置样例及基线中的 33 篇 Markdown 文档；外部对照采用固定提交的源码和官方文档。没有将源码可构建、文档标称和本人账号实机验证混为一谈。

## 调研范围与证据

固定检查了 6 个仓库的 12 个源文件。以下链接固定到审查时的提交，避免主分支变化后无法复核。

| 项目与提交 | 阅读位置 | 适合本仓库的做法 |
|---|---|---|
| New API `7fb6e57a5ff671582131c74e00bdfaa867d4e985` | [健康面板](https://github.com/QuantumNous/new-api/blob/7fb6e57a5ff671582131c74e00bdfaa867d4e985/web/src/features/dashboard/components/overview/performance-health-panel.tsx)、[日志统计卡片](https://github.com/QuantumNous/new-api/blob/7fb6e57a5ff671582131c74e00bdfaa867d4e985/web/src/features/dashboard/components/models/log-stat-cards.tsx) | 将健康与用量分开，并让统计范围跟随明确的时间筛选 |
| Sub2API `dba770d56a0ee0ae5bad1b3dc5e11614d323163c` | [请求详情](https://github.com/Wei-Shaw/sub2api/blob/dba770d56a0ee0ae5bad1b3dc5e11614d323163c/frontend/src/views/admin/ops/components/OpsRequestDetailsModal.vue)、[延迟分布](https://github.com/Wei-Shaw/sub2api/blob/dba770d56a0ee0ae5bad1b3dc5e11614d323163c/frontend/src/views/admin/ops/components/OpsLatencyChart.vue)、[用量表格](https://github.com/Wei-Shaw/sub2api/blob/dba770d56a0ee0ae5bad1b3dc5e11614d323163c/frontend/src/components/admin/usage/UsageTable.vue) | 区分首段与完整耗时，缓存读写作为单独事实，空数据不伪装成测得零 |
| LiteLLM `f2118fc7670ba3fa736d9b8f4deef9077a45a720` | [请求列定义](https://github.com/BerriAI/litellm/blob/f2118fc7670ba3fa736d9b8f4deef9077a45a720/ui/litellm-dashboard/src/components/logs/request/RequestLogsTableColumns.tsx)、[时间工具栏](https://github.com/BerriAI/litellm/blob/f2118fc7670ba3fa736d9b8f4deef9077a45a720/ui/litellm-dashboard/src/components/logs/request/LogsTableToolbar.tsx) | 请求表明确展示耗时与首段等待，筛选与排序集中管理 |
| Helicone `6b057cdaf302c7b79b1f1e99cbf1c26132bd59de` | [请求抽屉](https://github.com/Helicone/helicone/blob/6b057cdaf302c7b79b1f1e99cbf1c26132bd59de/web/components/templates/requests/RequestDrawer.tsx)、[空状态](https://github.com/Helicone/helicone/blob/6b057cdaf302c7b79b1f1e99cbf1c26132bd59de/web/components/templates/dashboard/DashboardEmptyState.tsx) | 从概览进入单次详情，完整保留缓存字段；本项目空白页不填入演示统计 |
| Langfuse `c83198b03f6b63e7d57c0daf016bb4d3ab052913` | [延迟图](https://github.com/langfuse/langfuse/blob/c83198b03f6b63e7d57c0daf016bb4d3ab052913/web/src/features/dashboard/components/LatencyChart.tsx)、[观测表状态](https://github.com/langfuse/langfuse/blob/c83198b03f6b63e7d57c0daf016bb4d3ab052913/web/src/features/tracing-tables/observations/ObservationsTableStoreProvider.tsx) | 图表、筛选和详情使用同一数据范围，明确空值及样本限制 |
| Portkey Gateway `669825cbe89ee51569918b8f78a9db486fd69dd4` | [公开能力与配置](https://github.com/Portkey-AI/gateway/blob/669825cbe89ee51569918b8f78a9db486fd69dd4/README.md) | 超时、重试和观测分开表达；公开仓库主要是网关，不能把产品宣传中的后台能力视为可直接复用的开源 UI |

补充阅读：

- [LiteLLM 日志说明](https://docs.litellm.ai/docs/proxy/ui_logs)：默认不保存请求 / 响应内容。用于本项目诊断摘要的字段边界。
- [Langfuse 指标](https://langfuse.com/docs/metrics/overview)与[表格图表的筛选限制](https://langfuse.com/docs/observability/features/events-table-charts)：无法应用的筛选需要明确说明。用于区分本项目累计量、时间范围和最近样本。
- [Grafana 面板实践](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/best-practices/)与[空值、刷新问题](https://grafana.com/docs/grafana/latest/visualizations/dashboards/troubleshoot-dashboards/)：按使用问题组织面板，减少重复展示，不把缺失数据连成确定趋势。
- [Prometheus 观测原则](https://prometheus.io/docs/practices/instrumentation/)：按服务阶段观察请求、错误和延迟。本项目沿用现有无外部依赖的采集方式。
- [Diátaxis 文档体系](https://diataxis.fr/)：区分首次教程、任务操作、参考和解释。本轮在原有 Markdown 仓库中应用这一组织方式。

以上是设计对照，取舍由本项目的数据与交付方式决定；没有复制其他项目代码、引入其框架，也没有连接第三方日志或分析服务。

## 全仓结论

| 范围 | 当前状态 | 本轮处理 |
|---|---|---|
| 协议分层 | `api` 与具体上游通过 IR 对接，三个客户端协议共享平台实现 | 保持分层；传输观察放在适配器公共层，不把 HTTP 依赖放进 IR |
| 超时与重试 | 前轮已修复 WorkBuddy 大请求的 60 秒回归，共享预算有离线覆盖 | 本轮只增加观察，不改变任何生成超时或尝试策略 |
| 免费策略 | ZCode 与接入页只配置免费模型，但模型测试页仍包含付费项、裸名和遗留付费默认值 | 测试选项、旧选择、跳转及发送前均校验当前免费状态，固定平台路由 |
| 调用统计 | 最近样本有 200 条上限，累计数含历史取消，容易误读为当前故障 | 增加近 15 分钟样本入口，失败与取消分开；明确样本范围 |
| 接收等待 | 长上传和首段等待曾统一显示“连接上游” | HTTP 写出回调区分上传与等待响应，迟到回调不能覆盖正在接收的流 |
| 延迟与缓存 | 后端已有首段等待和缓存字段，列表没有充分利用 | 展示 P95 首段等待、测量数量和按相同样本加权的缓存比例 |
| 用量历史 | 既有均摊功能能守住总量，但估算标识藏在方法说明 | 趋势、日历、读数明确标记；保留原始数据和既定分摊归属 |
| 页面密度 | 年度日历在主趋势前占据较多空间，定时刷新重建大量隐藏格子 | 日历移到趋势后，按需展开时渲染；保留展开状态与焦点 |
| 诊断与隐私 | 原始错误可能含上游回显，不适合直接复制整页 | 增加允许字段列表的诊断摘要，排除账号、正文、请求头及路径 |
| 配置可见性 | 后端已返回有效预算，设置页只显示旧超时项 | 展示实际预算、尝试数及队列设置，说明何时才会排队 |
| 平台操作能力 | 设置页给不支持脱敏的直连平台也展示开关 | 管理 API 明确返回适配器能力，页面隐藏不适用操作并兼容旧后端 |
| 文档 | 当前说明、历史设计与研究记录交叉引用；缺少完整运行和指标手册 | 重组导航，新增运行、指标和开发手册，修订现有指南 |
| 文档治理 | 缺少自动检查本地路径、页内标题及代码块的流程 | 增加标准库离线检查与 CI，保留日期明确的历史文档 |

### 需要纠正的表述

- 号池 `blocked` 是刷新后仍鉴权失败的本地调度状态，不能写成“账号死亡”或据此断言被封。
- 额度错误按模型和上游恢复时刻处理，不能概括为所有额度耗尽都永远不冷却或整号永久冻结。
- 只更新磁盘控制台不会升级后端；反过来，只拉取源码也不会替换运行中的嵌入资源。
- 本地成功的小提示只能验证兼容链路，不能单独证明多图片长请求、真实缓存或所有上游平台都正常。

## 界面组织

用量页依次展示累计规模、当前接收状态、所选日期和平台的趋势，再按需展开年度活动和累计分组。累计总量不跟随日期过滤；时间筛选只作用于对应的每日视图，并在说明中写清。

调用日志先回答“最近是否还在失败”，再提供筛选、样本分位数和单次详情。客户端取消以中性状态显示；原始计数保持不变。未知原因只显示已有事实，不从 `request illegal` 自动推断图片超限或封号。

模型测试只展示已确认免费且明确平台的路由，不将名字中的 `free` 作为免费依据。上游目录仍可显示其他模型用于对照；测试与客户端配置不会因此选择它们。

## 文档组织

| 读者任务 | 主要入口 |
|---|---|
| 第一次获得真实回复 | [快速开始](../quickstart.md) |
| 接入 ZCode / 其他客户端 | [客户端接入](../clients.md) |
| 更新、回退和保留历史 | [运行与升级](../operations.md) |
| 理解 Token、缓存、P95 与实时状态 | [指标口径](../metrics.md) |
| 查字段和端点 | [配置](../configuration.md)、[API](../api.md) |
| 修改代码或补平台 | [开发导览](../development.md)、[架构](../architecture.md) |
| 复核日期明确的结论 | [调研索引](../research/README.md)、[历史设计](../design/README.md) |

原文路径保留，避免外部引用失效。新检查器检查仓库内部链接、中文标题锚点、重复标题和未关闭代码块；它不联网断言外部服务状态。配置及能力说明仍需对照源码和真实验证维护。

## 验证

- 离线检查：Go 包测试 / vet；前端请求分类、分位数、缓存分母、隐私摘要、免费路由、懒加载日历和迟到回调测试。
- 浏览器：独立控制台连接既有网关，核对范围标记、筛选联动、详情、免费选项、展开状态、深浅主题及约 1093 / 1270 宽的桌面布局；手机实机尚未验证。
- 文档：本地目标、页内锚点、代码块及检查器自身回归测试，命令与默认值交叉核对。
- 发布：先构建候选程序，使用同一配置和统计路径，在无在途请求时切换；验证现有客户端继续调用。

完整测试命令见[贡献指南](../../CONTRIBUTING.md#构建与测试)。没有把个人配置、凭证、私有对话、真实截图或下载的参考源码提交到仓库。

本地覆盖率检查显示，`internal/app` 约 73%、`internal/obs` 约 90%、OpenRouter 约 88%，CLI 装配约 25%、静态资源包装层仍无 Go 覆盖。前端的离线数据测试与浏览器验证不能代替这两部分的启动和发布集成覆盖；后续应优先补真实风险场景，而不是为短小视图逐行堆测试。

## 仍需独立评估的工作

| 优先级 | 后续工作 | 为什么不直接堆进本轮 |
|---|---|---|
| 高 | 按天查询完整日志与延迟分布 | 当前 200 条样本无法支撑全历史 P95；需要先设计有界存储、分页和迁移 |
| 高 | 更完整的长上传、模型预处理与断流故障注入 | 本轮传输观察不改变计时策略；扩大故障场景应单独验证取消和健康度 |
| 中 | 继续拆分 `app.js` 页面装配 | 本轮已提取新统计逻辑，整站事件与状态迁移需专门的浏览器回归 |
| 中 | 非 Windows 桌面凭证端到端验证 | CI 构建与竞态通过无法替代真实系统、安装方式和账号验证 |
| 中 | 上游能力、免费目录变化的版本化样本 | 私有协议与权益会变化，需要持续维护可复核且脱敏的事实 |
| 低 | 可选长期指标存储和外部可观测性适配 | 保持默认单文件交付；引入数据库或外发日志需要明确部署与隐私需求 |

这是有范围的审查与改进记录，不代表代码不存在其他问题，也不对任何平台未来的免费额度作保证。
