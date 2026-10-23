# 模型目录、客户端同步与连接收尾审查

[文档导航](../README.md) · [开发导览](../development.md) · [上一轮全项目审查](2026-10-24-project-optimization-review.md)

审查日期：2026-10-24。基线：`cd78a99`。本轮沿上一轮的取消与生命周期检查，继续追踪目录刷新、客户端配置和连接结束时的竞争条件。下面只记录有离线复现和对应回归的增量改动；上一轮的协议与性能结论见前一份报告。

## 对照依据

| 来源 | 核对内容 | 本轮采用方式 |
|---|---|---|
| [Go singleflight](https://pkg.go.dev/golang.org/x/sync/singleflight) | 同一键的重复请求共享执行，独立键不应共用全局等待 | Hub 以平台为键协调刷新，保留主动刷新必须重新核验的语义；没有新增依赖 |
| [WHATWG SSE 解析规则](https://html.spec.whatwg.org/multipage/server-sent-events.html#parsing-an-event-stream) | CR、LF、CRLF 都能结束一行，空行触发事件 | 控制台逐行解析兼容三种分隔及跨网络块的 CRLF；保留现有 JSON 流的 EOF 兼容处理 |
| [Go Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown) 与 [Close](https://pkg.go.dev/net/http#Server.Close) | Shutdown 到期返回不表示活跃连接已关闭；Close 也不替应用等待处理器结算 | 显式跟踪请求收尾，关闭连接后等待处理器退出，再保存最终指标 |

## 1. 每个平台独立刷新目录

原来的 Hub 只有一个全局刷新任务。一个慢平台会挡住另一个平台的单独刷新；整批更新也要等最慢平台完成才发布结果。客户端同步还把“任何平台仍在刷新”当成拒绝写入的条件，导致已选平台正常时仍被无关平台阻塞。

现在每个平台有自己的刷新任务。普通并发读取复用该平台的结果；主动刷新等待该平台已有任务后重新核验。单个平台完成便更新自己的目录和状态，其他平台继续执行。客户端同步只检查选中平台。失败保留旧目录，但记录失败状态，旧目录不会因此变成新确认的免费权益。

启动时各平台同时枚举，共用 20 秒预算，避免逐个平台累计等待。到期刷新也按平台协调：已有平台在查时，其他平台仍能发起检查；完整刷新不再错误地仅等待已有任务而漏查其他平台。发起刷新的 context 到期或取消后，Hub 释放对应占位，迟到结果不覆盖已结束的状态。

实现与回归：[catalog.go](../../internal/app/catalog.go)、[hub.go](../../internal/app/hub.go)、[clients.go](../../internal/app/clients.go)、[catalog_test.go](../../internal/app/catalog_test.go)。测试覆盖并行启动、跨平台隔离、完整刷新覆盖、超时保留与定时刷新。

## 2. OpenRouter 的等待、失效与缓存归属

目录查询与保存密钥原来通过普通互斥锁串行执行，排队者取消后仍要等前一个网络请求结束。改为可监听 context 的执行入口，目录查询的超时预算同时覆盖排队与网络请求。

另一个竞争发生在“请求发出后点击刷新”：旧请求回来时仍可重新填入已失效缓存。现在失效和密钥更新推进目录版本；返回值属于旧版本时重新查询，避免把刚变为付费的模型继续当成免费。返回目录也复制定价结构，调用者对结果的修改不会污染适配器缓存。

实现：[adapter.go](../../internal/adapter/openrouter/adapter.go)、[credentials.go](../../internal/adapter/openrouter/credentials.go)。离线回归验证取消等待、查询过程中价格从免费变付费、结果修改隔离，以及并发读取共享一次查询。

## 3. 取消与连接结束保留正确结果

- **Cursor**：读取凭证期间取消，原来可能包装为需要重新登录。现在在读取前后识别 context 状态；主动取消保留取消分类，目录查询被取消也不会清空已确认缓存。真实鉴权失败仍按鉴权失败处理。
- **Kiro**：CLI 标准输出关闭或超过帧大小限制后，读取循环先等待进程退出，可能留下仍在等待输入的 CLI 和全部 RPC。现在结束进程并释放等待者；若完整回复已经到达，优先交付该回复，避免回复与 EOF 同时到达时随机误报连接关闭。
- **TRAE**：Close 停止计时器之后，读取循环处理已缓冲的一行仍可能重新启动计时器。关闭状态与重置操作现在由同一把锁协调，已关闭流不会再次启动空闲超时。

实现：[Cursor](../../internal/adapter/cursor/adapter.go)、[Kiro RPC](../../internal/adapter/kiro/runtime.go)、[TRAE 流](../../internal/adapter/trae/stream.go)。对应测试使用本地假响应、受控管道或子进程，不消耗真实模型额度。

## 4. ZCode 两种同步入口保持一致

Python 脚本已经定义 Kiro 未公布输出上限时的 8192 建议预算，但筛选条件仍要求上游上限大于零。因此同一个免费 Kiro 模型可通过控制台同步，却会被脚本拒绝或移除。筛选现在使用通过免费权益验证后的客户端预算，保留上游原始未知值。回归覆盖新增、保留禁用状态、自定义较小预算和重复同步不重写。

脚本此前也没有使用控制台的同步锁。现在两个入口共用 `.agent2api.lock`；首次创建配置使用独占发布，即使客户端刚好创建了文件也不会覆盖。已有文件继续在写入前比对原始内容，并先备份再替换。

实现与回归：[sync-zcode.py](../../scripts/sync-zcode.py)、[test_sync_zcode.py](../../scripts/test_sync_zcode.py)。本轮仅操作临时测试配置，未写入用户的客户端设置、会话或统计数据库。

## 5. 控制台不再丢失 CR 结尾的最后事件

旧解析器只按 LF 分行，再去除行尾 CR。遇到 `data: {...}\n\r` 时，EOF 分支虽然解析出最后事件，却丢弃了返回值；只有 CR 分隔的多个事件也不能正确处理。

现在直接识别 CR、LF 和 CRLF，记录跨块 CRLF 中需要跳过的 LF。CR 已结束空行时立即交付事件；`[DONE]` 后取消并释放 reader，不等下一块网络数据。回归覆盖最后 usage 帧、仅 CR 分隔、逐字节 UTF-8 / CRLF、取消和错误 JSON。

实现与回归：[core.js](../../internal/web/static/core.js)、[test-console-core.mjs](../../scripts/test-console-core.mjs)。这是流读取修复，页面布局和指标计算公式没有变化。

## 6. 强制关闭后的最终统计

已有优雅关闭路径会等待正常请求完成。本轮进一步处理 15 秒 Shutdown 预算用尽的情况：停止接收新请求，关闭仍活跃的连接，再给处理器最多 5 秒完成取消和统计收尾，最后保存快照。异常处理器若不响应取消，超过收尾预算后仍保存当前快照并返回错误，不保证这类处理器全部完成。

实现与回归位于 [shutdown.go](../../cmd/agent2api/shutdown.go) 和 [shutdown_test.go](../../cmd/agent2api/shutdown_test.go)。已验证正常排空，以及超时强制关闭后把最后请求结算纳入持久化结果。

## 验证与后续

从暂存区导出本轮提交内容后，完整本地验证通过：19 个 Go 包的构建、`go vet` 和测试，92 项前端测试、49 项 Python 测试，以及 45 份文档的 493 个本地链接检查。未提交的其他工作不参与这份验证。

目录协调、适配器取消、CLI 退出、客户端同步和前端解析都有离线回归；Shutdown、Kiro RPC 与 TRAE 计时器还经过重复执行检查。跨平台竞态与兼容性结果见该提交的 [GitHub CI](https://github.com/576469377/Agent2API/actions/workflows/ci.yml)。本轮验证均使用离线数据或本地受控响应，没有重启正在使用的网关。

后续独立检查 WorkBuddy 认证锁的持有范围与后台 context，以及指标周期保存任务的退出 context。本轮没有为这些尚未完成的审查扩大代码改动。
