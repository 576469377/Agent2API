# 账号积分、额度与自动签到

[文档导航](README.md) · [账号与调度](accounts.md) · [指标口径](metrics.md)

## 显示范围

账号列表、卡片与详情展示适配器实际读取到的上游信息，各账号独立缓存和刷新。积分、美元预算、模型倍率和 Token 统计是不同口径；不会相互换算，也不按本地请求记录推算官方余额。

| 平台 | 可读取的数据 | 自动签到 |
|---|---|---|
| WorkBuddy 国内版 | 上游积分套餐的总额、已用与剩余 | 支持状态查询和每日领取；只在上游确认活动开放且今日未签到时领取 |
| WorkBuddy 国际版 | 同区域账号的积分套餐汇总 | 使用国际站自己的状态；2026-10-23 本机查询为活动未开放，因此跳过，不借用国内站签到 |
| OpenRouter | 账户美元余额、可见的 API Key 预算与今日美元消耗 | 未接入独立签到；免费模型的每日请求余量没有可核实的数值时保持未知 |
| Cursor | 上游本期套餐用量；明确返回时显示余额和重置时间 | 未接入独立签到；没有返回 Free 剩余额度时不显示为零 |

WorkBuddy 套餐积分按官方客户端使用的资源汇总接口读取；OpenRouter 按官方 [`/credits`](https://openrouter.ai/docs/api/api-reference/credits/get-remaining-credits) 与 [`/key`](https://openrouter.ai/docs/api/api-reference/api-keys/get-current-api-key) 接口读取，普通 Key 可能没有账户级查询权限。Cursor 套餐计量按官方客户端的美分字段换算为美元，没有根据模型调用次数猜测金额或剩余 Token。

数值为零仅在上游确实返回零时展示。缺失、读取失败和上次读数分别标记，失败刷新保留上次有效余额及读取时间；较旧数据标为“上次读数”。已存在的某种积分不表示任何付费模型可用，原有免费路由继续执行。

## 后台行为

- 每个已接入账号独立运行。余额默认最多约每 5 分钟刷新，查询并发最多为 3；列表本身只读取缓存。
- WorkBuddy 默认每天北京时间 09:00 自动检查签到；可在账号详情或“每日自动签到”展开区修改时间或暂停。
- 网关需要保持运行，网页可以关闭。错过当天执行时间后，网关启动会先查今日状态，再决定是否补签。
- 活动未开放、今日已签到或状态无法确认时不盲目领取。WorkBuddy 失败后间隔 15 分钟重试，每个账号每天最多尝试 6 次领取。
- WorkBuddy 领取前先保存尝试状态，领取后再次检查；即使领取响应丢失，也先检查今日状态，避免直接重发。状态无法保存时不发出领取请求。
- 账号之间不共用余额、签到日期和领取结果。重新登录后，后台扫描会跟随新的适配器；移除账号后停止对应后台任务。

自动领取仅调用已确认的签到接口，不会发送模型对话来伪造活跃奖励，不会修改付费、订阅或按量开关。需要官方客户端验证或重新登录时显示失败，保留账号数据，由用户在官方客户端恢复。

## 管理接口

以下接口使用与其他管理 API 相同的鉴权。号池有多个账号时，`platform` 与 `label` 必须同时指定；不存在或含糊的账号会被拒绝，不会退回池中的第一个账号。

```http
GET /api/accounts/benefits?platform=workbuddy&label=example.json
GET /api/accounts/benefits?platform=workbuddy&label=example.json&refresh=1
GET /api/accounts/checkin?platform=workbuddy&label=example.json
GET /api/accounts/checkin?platform=workbuddy&label=example.json&refresh=1
```

不带 `refresh=1` 时只读缓存；刷新签到状态不领取奖励。显式领取使用 `POST /api/accounts/checkin?...`，JSON 为 `{"action":"claim"}`；设置用 `{"action":"configure","enabled":true,"time":"09:00"}`。配置只作用于选中的账号。

`/api/accounts/manage` 的每个账号可包含 `benefits` 与 `checkin`。余额字段为 `total`、`used`、`remaining`，缺失字段省略；`unit` 为 `credits` 或 `USD`。这些响应不包含访问令牌、刷新令牌或原始上游响应。

WorkBuddy 的签到状态保存于凭证文件旁的 `.checkin-state` 文件，内容仅包含设置、账号摘要和签到结果。它们不是凭证备份，也不是网关调用历史，不应提交到公开仓库。
