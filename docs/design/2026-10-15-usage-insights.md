# 用量统计：ZCode 参考与实现决策

## 参考版本

调研日期：2026-10-15。ZCode 固定版本 [`aac4755`](https://github.com/zai-org/ZCode/tree/aac4755666d09fdcd70272fcf063c077a639015f)。阅读公开代码，仅参考交互与统计口径，本项目使用原有 Go 指标与原生 SVG / CSS 独立实现。

| 阅读来源 | 参考内容 | Agent2API 的处理 |
|---|---|---|
| [每日模型曲线](https://github.com/zai-org/ZCode/blob/aac4755666d09fdcd70272fcf063c077a639015f/packages/ui/src/settings/usage-stats/AppUsageDailyModelTrendChart.tsx) | 连续每日序列、前六个模型、可见曲线峰值决定纵轴、悬停明细 | 按实际平台 / 模型组合分组，加入其他模型保证用量不漏计；未知日期断开曲线，支持键盘逐日查看 |
| [模型环图](https://github.com/zai-org/ZCode/blob/aac4755666d09fdcd70272fcf063c077a639015f/packages/ui/src/settings/usage-stats/AppUsageModelUsagePieChart.tsx) | Token 占比与模型用量明细 | 使用所选日期范围和平台的输入 + 输出 Token，不沿用旧页面的调用次数分母 |
| [热力图](https://github.com/zai-org/ZCode/blob/aac4755666d09fdcd70272fcf063c077a639015f/packages/ui/src/settings/usage-stats/UsageHeatmap.tsx) | 周列、月份刻度、每日活动详情 | 日历显示近 366 天，可切换 Token / 调用；未保留历史与实际零用量分开，点击日期查看实际模型 |
| [用量面板](https://github.com/zai-org/ZCode/blob/aac4755666d09fdcd70272fcf063c077a639015f/packages/ui/src/settings/usage-stats/AppUsagePanel.tsx) | 分段时间范围、趋势与环图同步 | 提供近 7 天 / 30 天 / 保留历史，不声称可以恢复旧版没有保存的全部日期 |
| [统计构造器](https://github.com/zai-org/ZCode/blob/aac4755666d09fdcd70272fcf063c077a639015f/apps/zcode-cli/packages/bootstrap/src/zcode-protocol/usage-stats-builder.ts) | 日聚合、活跃天数、输入统计避免重复计算缓存 | 网关没有客户端会话时长，故不展示最长聊天时长；日界使用明确 UTC，思考 Token 不再加到输出之外 |

## 数据与升级

原有累计指标和最近 200 条日志不变。新增长期日历聚合，每日按实际平台 / 模型存储调用、成功、失败、输入 / 输出 Token 与耗时总量，保留 366 个日历日，并与指标文件一起原子落盘。图表刷新不发送模型生成请求。

新文件保存完整采集开始时间。旧文件首次加载时从真实最近记录回填有限样本，保留此前累计请求数作为覆盖说明；不按比例分配累计 Token，不重复导入，也不读取或修改 ZCode 本地数据库、凭证或会话。

旧后端继续运行时，静态页面可展示最近记录样本并明确标注；需要部署新版后端才能启用长期日历史。独立预览与模拟数据用于检验视觉、交互和空态，不写入运行中的指标文件。

## 验收

- 按日数据超过 200 个请求仍完整；落盘、重启和续记不丢失或重复。
- 同名模型跨平台隔离；UTC 日界、乱序记录、366 天窗口与旧文件回填均有测试。
- 未知、部分与完整覆盖分开；曲线、环图和明细同范围，其他模型占比保留。
- 图例开关、平台筛选、7 / 30 天范围、日期详情、键盘交互、深浅主题与手机布局通过浏览器验收。
- 仅使用指标元数据；不提交本地配置、凭证、运行指标、参考下载或验收截图。

## 2026-10-15 补充：模式、历史恢复与实时采集

进一步阅读 `UsageHeatmap.tsx` 的 daily / weekly / cumulative 构造：自然周对齐、按周汇总、逐周累计，七行填充表示相对用量。Agent2API 补齐三种可选模式，保留未知 / 部分覆盖，不照搬将缺失历史补零的行为。

新 `/api/live` 观察现有响应事件，仅累加字符计数，不改变转发事件或保存正文。已有同平台 / 模型结算样本时提供明确估算值；无样本时显示真实字符速度。通过受控流验证流式与非流式处理进行中可观测、结束移除且最终 Token 不变。

本地旧指标文件与日志可按显式配置只读导入。早期累计快照和当前总量的交集不可确认，因此单独列示；只恢复有日期的记录，日志没有 Token 就标未知，来源与记录均去重。未导入的日期不表示从未使用。
