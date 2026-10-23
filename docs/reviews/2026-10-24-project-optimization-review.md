# 全项目分析与性能优化

[文档导航](../README.md) · [开发导览](../development.md) · [上一轮审查](2026-10-23-console-and-repository-review.md)

审查日期：2026-10-24。基线：`c85c0e2`。本轮按请求生命周期、协议契约、指标采集、平台边界和交付流程检查 19 个 Go 包，并交叉阅读前端模块、维护脚本、配置及 CI。对关键路径做源码审查、离线复现和基准；测试通过不等于所有上游都经过真实账号验证。

实际落地集中在兼容性、阻塞取消和已有热点：修复 Responses 缺失事件与非流式内容重排，打通 OpenRouter / Kiro 的取消，纠正 TRAE 取消分类，减少长流和统计的分配与锁竞争，复用前端派生数据，实现静态文件条件缓存，并增加 Windows CI。没有改动免费模型政策或历史统计格式。

## 外部项目与取舍

参考源码固定到本次查询的提交；文档链接记录的是本次阅读位置。采用适合单机 Go 网关的实现，不引入这些项目的框架或依赖。

| 来源 | 具体检查 | 在 Agent2API 中的取舍 |
|---|---|---|
| [Bifrost 请求调度与收尾](https://github.com/maximhq/bifrost/blob/14e33c8dee6902a34d2d50292824c8777c08fdee/core/bifrost.go) | 排队取消、请求交接、放弃接收后的资源归属、流式收尾 | 沿着取消信号检查阻塞点和释放责任；本仓库保留已有同步 IR，不引入额外工作池或对象池 |
| [Portkey 重试处理](https://github.com/Portkey-AI/gateway/blob/669825cbe89ee51569918b8f78a9db486fd69dd4/src/handlers/retryHandler.ts) | 重试次数、总等待预算、429 恢复提示、建连超时 | 保留现有共享尝试预算与准确的 Retry-After；没有扩大自动重试，以免放大免费额度消耗 |
| [New API 适配器接口](https://github.com/QuantumNous/new-api/blob/33c61a24234aa20cb10e8a644304322c476361de/relay/channel/adapter.go) | 多协议请求转换、上游执行、响应解析与用量边界 | 本项目三协议继续复用统一请求 / 事件 IR；平台私有逻辑留在适配器，不增加协议与平台的交叉实现 |
| [LiteLLM 超时](https://docs.litellm.ai/docs/proxy/timeout)与[回退](https://docs.litellm.ai/docs/proxy/reliability) | 首段等待、总超时、失败回退的不同职责 | 保留当前按阶段计时、响应开始后不重放的约束；本轮修的是超时无法中断阻塞 I/O，不是单纯延长等待时间 |
| [官方 Python Responses 聚合器](https://github.com/openai/openai-python/blob/v2.36.0/src/openai/lib/streaming/responses/_responses.py) | 内容块初始化、增量索引、序号、文本与工具结束事件 | 用本机 `openai-python 2.36.0` 离线消费真实编码器输出，先复现异常，再验证修复 |
| [Prometheus 客户端设计](https://prometheus.io/docs/instrumenting/writing_clientlibs/)与[指标实践](https://prometheus.io/docs/practices/instrumentation/) | 采集与更新竞争、关键操作基准、维度基数 | 一致快照在锁内复制，排序移到锁外；保留历史，不用删除数据换取速度 |
| [OpenTelemetry Metrics SDK](https://opentelemetry.io/docs/specs/otel/metrics/sdk/) | 采集和导出的串行及一致性 | 已有存储协调器串行保存，不再添加重复的存储锁；补充历史与累计快照并发一致性断言 |
| [Go ServeContent](https://pkg.go.dev/net/http#ServeContent)与[MDN Cache-Control](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Cache-Control) | ETag 条件请求、304、HEAD、Range，及 no-cache 的重验证含义 | 对嵌入式内容预计算 ETag，使用标准库处理条件与范围请求；升级后内容变化立即失效 |

这些对照用于发现实现差距。其他项目的宣传性能不能作为本项目性能，下面的数字均来自本地合成基准。

## 审查覆盖与结论

| 范围 | 本次检查 | 处理结果 |
|---|---|---|
| `internal/api`、`internal/llm` | 三协议流 / 非流、工具、思考、结束原因、统一失败类型 | Responses 有可复现兼容缺陷；Chat、Messages 与 IR 的现有回归通过 |
| `internal/app` 请求链 | 建流预算、流接受后停表、首事件、HTTP flush、断开统计、释放上游 | 修复只提供 `Unwrap` / `FlushError` 的 HTTP 包装器不能及时刷新；沿用原有路由预算 |
| `internal/adapter` 号池 | 选号占槽、排队取消、限流与鉴权分类、模型亲和、释放 | 本轮现有容量 / 健康 / 超时测试通过，无证据要求改动调度策略 |
| WorkBuddy 两区 | 区域分离、会话亲和、流取消、目录与保活路径 | 维护后台任务生命周期；另一项尚未提交的客户端隔离工作不混入本轮 |
| OpenRouter | 免费目录、零价约束、流式读取与取消 | 接收上下文现在能打断静默阻塞读 |
| Cursor | Free 权益校验、请求 / 工具转换、流与进程收尾 | 离线回归通过，本轮没有修改认证、权益或真实额度 |
| TRAE | Free 积分来源、目录、流式事件、取消、签到任务边界 | 取消期间阻塞读返回取消原因，不再记成上游流损坏 |
| Kiro | CLI RPC、stdin 排队与大帧、关闭、免费与 overage 判断 | 写入和等待写入都可取消；已写入半帧的连接关闭，防止继续解析损坏帧 |
| `internal/obs` | 累计、日期、最近样本、实时窗口、历史导入与落盘 | 排序移出锁、同值行稳定排序；计数和缺失用量语义保留 |
| `internal/web` | 页面资源、用量派生、缓存失效、静态交付 | 复用相同指标快照的派生日历，增加 HTTP 条件缓存 |
| `internal/clientconfig`、`internal/zcode` | 免费筛选、增量合并、原子替换、备份、重复同步与并发修改 | 现有保留配置及历史的测试通过；本轮不写用户客户端配置 |
| `internal/config`、维护脚本 | 默认值、平台继承、请求预算、诊断脱敏、文档 / 凭证检查 | 沿用既有约束；校正文档中过时的余额查询与包地图说明 |
| `cmd/agent2api`、`cmd/console` | 保活、目录监视、关闭、磁盘界面代理和 SSE | 后台任务随服务取消；独立控制台代理现有回归通过 |
| 构建与 CI | 平台矩阵、测试范围、lint 返回码、清理目标 | 添加 Windows stable；Make 只处理源码目录并保留其他本地资料 |

## 具体修复

### 1. Responses 流和 SDK 聚合一致

之前发出 `response.output_item.added` 时，message 的 `content` 是空数组，紧接着就发送 `response.output_text.delta`。官方 SDK 根据 `content_index` 索引内容块，离线重放可触发 `IndexError`。同时缺少 `sequence_number` 及一组内容结束事件。

现在文本、思考摘要与工具参数都有完整的开始 / 增量 / 结束事件，事件序号连续递增。工具名称继续按当前请求的 namespace 映射恢复。长度与内容过滤分别返回 `response.incomplete` 和原因，错误返回 failed，正常完成返回 completed。

非流式编码器原来把所有文本聚合后放到末尾，思考输出为空 summary；现按 IR 内容顺序输出，保留思考文本及工具两侧的文本块。新增用例覆盖内容次序和结束原因，不改变工具调用与续轮的身份映射。

对应代码：[Responses 编码器](../../internal/api/openai/responses/responses.go)、[生命周期测试](../../internal/api/openai/responses/lifecycle_test.go)。

### 2. 取消能够终止实际阻塞

- **OpenRouter：** 原来只在开始读取前检查接收上下文，阻塞中的 `Scanner.Scan` 不会被后续取消唤醒。现在取消会关闭该响应体，`Recv` 返回取消 / 超时原因，关闭保持幂等。
- **Kiro：** 原来 RPC 在进入等待响应的 `select` 前同步写 stdin，大请求被 CLI 阻塞时，超时还没有机会生效。现在排队写入和实际写入均接受调用上下文。排队者取消不影响当前帧；已经开始写入的调用取消时结束该连接，因为半截 JSON 帧无法安全复用。其他等待者得到连接已关闭的错误。
- **TRAE：** 读期间关闭响应体后，原实现把取消归为流缺少终止帧。现在优先返回上下文原因，避免将客户端取消记为上游失败。

测试使用本地管道、HTTP 测试服务与握手信号，不依赖真实账号或网络生成。旧实现已分别复现阻塞超时和错误分类，修复后重复运行通过。

### 3. SSE 包装器和后台任务生命周期

HTTP 响应写入器如果只实现 `Unwrap` 或 `FlushError`，旧代码的 `http.Flusher` 断言会跳过刷新。现在统一交给 `http.ResponseController`，可穿过包装器刷新；真实刷新失败仍进入客户端断开统计。

WorkBuddy 保活原来先等待 15 分钟，再创建一个 15 分钟 ticker，首次执行实际要等 30 分钟。现由共享的周期执行函数等待一次间隔。保活和账号目录监视随服务上下文停止，不在关闭后继续启动新一轮；保活请求的独立 30 秒上限仍保留。

已开始的保活 / 目录扫描按原有边界自行收尾，本次没有增加全局等待后台任务退出，也没有让这些在途调用立即中断。

### 4. 统计快照不再持锁排序

快照仍在同一把锁下复制累计、分组、最近样本与每日数据，保持一致性。副本离开锁后才排序、填齐时间桶和编码，实时更新不必等待全年模型行排序。保存文件使用同一方式，原来的保存串行化继续生效。

分组原来只按调用数排序，相同调用数会随 map 遍历顺序跳动；现在再按分组身份排序。历史日期与模型顺序保持稳定。未知 Token 仍保留缺失信息，已有实测 Token、缓存读写和累计总量不变。

### 5. 控制台复用已计算的数据

图表悬浮、日期详情和同轮刷新原来重复调用 `buildUsage`，每次重新聚合全年数据。现在用 WeakMap 按 API 快照缓存，且每个快照只保留最后一组筛选；旧快照可以正常回收。

新快照、UTC 跨日、日期范围、平台或分配设置变化会重新计算。隐藏曲线和切换展示指标直接复用相同底层数据。纯计算函数保留，回归测试比较缓存与直接计算的结果，包括原地修改分配设置和跨日。

### 6. 静态资源的缓存校验真正生效

原来只有 `Cache-Control: no-cache`，嵌入式文件没有修改时间或 ETag，浏览器即使缓存了 JS / CSS，也要重新下载正文。现首次使用时遍历嵌入文件并计算 SHA-256 ETag，随后直接从原文件系统提供内容，不保留第二份完整资源副本。

内容相同返回 304；新程序的文件内容改变后返回新内容。入口 HTML 和模块导入都执行校验。HEAD、弱 ETag、多个条件标签、旧版本标签及 Range 由标准库处理并有测试。

### 7. 开发与交付

- Windows stable 加入构建、`go vet` 和测试；Linux / macOS 继续覆盖 Go 1.23 / stable 与 race。
- Make 的测试、格式化和静态检查限定 `cmd/`、`internal/`，避免把忽略的 `bin/` 临时研究 Go 程序纳入主项目。
- `make lint` 不再把检查失败显示为“未安装”并返回成功。
- `make clean` 只清理 Make 构建的程序与覆盖率文件，保留 `bin/` 中其他候选程序和诊断资料。

## 性能证据

环境：Windows amd64、Intel Core i9-13980HX。本地并行工作会影响耗时，以下是相同数据构造下的观察值，不是模型响应速度或部署容量承诺。

| 场景 | 修改前 | 修改后 | 含义 |
|---|---:|---:|---|
| Responses，128 KiB 文本分成 1,024 片 | 约 72.94 MB 分配 / 次 | 约 2.82 MB / 次 | 以 builder 代替逐片字符串拼接，减少约 96% 分配量 |
| 同上，编码耗时 | 约 25–44 ms | 约 4.1–5.1 ms | 包含完整事件编码；不是上游生成耗时 |
| 366 天 × 64 模型，另一 goroutine 连续抓快照时 Record 中位耗时 | 71.641 µs | 0.772 µs | 极端连续抓取下减少锁竞争，不代表请求整体快 93 倍 |
| 同上，单次历史快照分配次数 | 1,482 | 378 | 使用泛型排序并减少排序闭包分配 |
| 同一全年数据重复构建派生用量 | 约 17.64 ms / 次 | 命中后低于 1 µs | 两条路径均为 35,136,000 合成 Tokens；首次和失效仍正常构建 |
| `app.js` 全文响应 / 条件命中处理 | 全文约 6.5–7.2 µs | 304 约 0.86–0.88 µs | 进程内 HTTP 基准；304 无正文，未计真实网络传输 |

已纳入仓库的基准可复跑：

```powershell
go test ./internal/api/openai/responses -run '^$' -bench BenchmarkLongTextStream -benchmem -count=3
go test ./internal/obs -run '^$' -bench 'Benchmark(SnapshotYearHistory|RecordDuringHistorySnapshots)$' -benchtime=2s -count=3
go test ./internal/web -run '^$' -bench BenchmarkConsoleRevalidation -benchmem -count=3
```

## 验证与范围

新增回归覆盖 Responses 事件与内容、真实 SDK 离线聚合、包装器 flush、阻塞取消、后台退出、快照一致性与顺序、前端缓存失效和静态条件缓存。

统一验证执行 Go 构建 / 测试 / vet、90 项前端测试、46 项 Python 测试、文档链接和暂存凭证扫描。最终提交还交给 GitHub 的 Linux、macOS、Windows 矩阵验证；每次运行状态以该提交的 Actions 为准。Lint 仍是现有的建议性检查，没有把允许失败的任务当作强制质量门禁。

本轮没有运行真实付费或免费生成、自动签到和账号登录，也没有重启生产网关或改写用户统计与客户端配置。上游可用性和实际剩余额度需要独立实测。该范围避免把离线验证结果误写为平台在线验证。

## 后续优先级

1. **持续保留性能基线。** 当前基准进入代码，可在较大历史或较长流出现时复跑，再决定是否做分页、压缩或独立历史存储。现有 JSON 格式继续兼容。
2. **用真实规模决定维度上限。** 累计模型 / 账号分组目前没有数量上限。大量动态模型可能增加快照成本；先观测基数和文件大小，再设计归档，不能直接丢弃用户统计。
3. **逐步拆分前端协调器。** `app.js` 仍较大。本轮把计算复用放在既有用量模块；后续新增页面应按模块边界实现，不必为了目录整齐一次重写所有事件处理。
4. **逐项收紧 lint。** 现有 lint 配置排除了部分测试分析且允许失败。应先处理明确告警，确认跨版本结果，再改为强制门禁。
5. **上游契约变化靠脱敏样本跟进。** Kiro 当前没有可用的 Token 用量、桌面平台接口与免费目录会变化。这些是实测与适配问题，不能用估算 Token 或更激进重试代替。

不引入 Redis、数据库迁移、通用插件框架或整套前端框架：本次已复现的问题可以在现有单文件交付方式下解决。
