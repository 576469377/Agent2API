package adapter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// fakeAd 是可编程假适配器：Stream 按预设返回错误或按序回放事件。
type fakeAd struct {
	name string
	err  error
	// limitModel 指定该账号上哪些模型返回限流错误（模拟上游按模型限流）。
	limitModel map[string]bool
	// events 非空时作为流的回放内容（用于首帧探针测试）。
	events []llm.ResponseEvent
	// onStream 在每次 Stream 被调用时执行（用于计数）。
	onStream func()
}

func (f *fakeAd) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	if f.onStream != nil {
		f.onStream()
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.limitModel != nil && f.limitModel[req.Model] {
		return nil, rateLimitedErr("model " + req.Model + " quota exceeded")
	}
	if len(f.events) > 0 {
		return &fakeStream{events: f.events}, nil
	}
	return &fakeStream{}, nil
}

type fakeStream struct {
	events []llm.ResponseEvent
	pos    int
}

func (s *fakeStream) Recv(context.Context) (llm.ResponseEvent, error) {
	if s.pos >= len(s.events) {
		return llm.ResponseEvent{}, llm.ErrStreamDone
	}
	ev := s.events[s.pos]
	s.pos++
	return ev, nil
}

func (s *fakeStream) Close() error { return nil }

func (f *fakeAd) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }
func (f *fakeAd) Name() string                                    { return f.name }

func rateLimitedErr(msg string) error { return rateLimitedFailure(msg) }

func rateLimitedFailure(msg string) *llm.Failure {
	f := llm.NewFailure("rate_limited", msg, nil)
	f.RateLimited = true
	return f
}

// TestPoolRotatesAcrossHealthyAccounts 验证轮询：连续请求均匀落到各账号。
func TestPoolRotatesAcrossHealthyAccounts(t *testing.T) {
	var hits [2]int
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", onStream: func() { hits[0]++ }})
	p.Add("b", &fakeAd{name: "test", onStream: func() { hits[1]++ }})

	for i := 0; i < 6; i++ {
		if _, err := p.Stream(context.Background(), llm.RequestMessages{}); err != nil {
			t.Fatalf("第 %d 次请求不应失败: %v", i, err)
		}
	}
	if hits[0] != 3 || hits[1] != 3 {
		t.Fatalf("轮询应均匀分布, hits=%v", hits)
	}
}

// TestPoolCooldownsRateLimitedAndFailsOver 是号池的核心场景：
// 一个账号被限流 → 冷却它 → 后续请求自动走其余账号。
func TestPoolCooldownsRateLimitedAndFailsOver(t *testing.T) {
	var hits [2]int
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: rateLimitedErr("额度耗尽"), onStream: func() { hits[0]++ }})
	p.Add("b", &fakeAd{name: "test", onStream: func() { hits[1]++ }})

	// 前若干次请求：首次先撞 a（限流→冷却）再 failover 到 b；之后全部直达 b。
	for i := 0; i < 4; i++ {
		s, err := p.Stream(context.Background(), llm.RequestMessages{})
		if err != nil {
			t.Fatalf("b 可用，请求不应失败: %v", err)
		}
		if closer, ok := s.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	if hits[0] != 1 {
		t.Fatalf("限流账号应在首次失败后被冷却, hits_a=%d", hits[0])
	}
	// 首次请求的 failover 记 1 次，其后 3 次直达，共 4 次。
	if hits[1] != 4 {
		t.Fatalf("健康账号应承接全部请求, hits_b=%d", hits[1])
	}
	// 账号本身仍健康（限流是模型级的），但 model-x 在该账号上已冷却。
	p.mu.Lock()
	accA := p.accounts[0]
	cooling := accA.modelCooling(time.Now(), "")
	acctUsable := accA.accountUsable(time.Now())
	p.mu.Unlock()
	if !cooling {
		t.Fatal("该账号上的空模型键应处于冷却（测试未传模型名）")
	}
	if !acctUsable {
		t.Fatal("账号不应因单模型限流而整体不健康")
	}
	if got := p.HealthyCount(); got != 2 {
		t.Fatalf("两个账号本身都应健康, got %d", got)
	}
}

// TestPoolCooldownExpires 验证冷却到期后账号自动回到轮询。
func TestPoolCooldownExpires(t *testing.T) {
	var hits int
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: rateLimitedErr("x"), onStream: func() { hits++ }})
	p.Add("b", &fakeAd{name: "test"})

	// 触发 a 上 model-x 的冷却。
	_, _ = p.Stream(context.Background(), llm.RequestMessages{Model: "model-x"})
	p.mu.Lock()
	coolingNow := p.accounts[0].modelCooling(time.Now(), "model-x")
	// 人为把该模型冷却拨回过去，模拟到期。
	p.accounts[0].modelCooldown["model-x"] = time.Now().Add(-time.Second)
	coolingAfter := p.accounts[0].modelCooling(time.Now(), "model-x")
	p.mu.Unlock()
	if !coolingNow {
		t.Fatal("model-x 应处于冷却中")
	}
	if coolingAfter {
		t.Fatal("冷却到期后该模型应恢复可用")
	}
	_ = hits
}

// TestPoolClientFixableFailsFast 验证参数类错误不换号不冷却，直接失败。
// 生产中 httpError(400) 会显式置 ClientFixable；这里模拟同一形状。
func TestPoolClientFixableFailsFast(t *testing.T) {
	bad := llm.NewFailure("bad_request", "参数错误", nil)
	bad.ClientFixable = true
	var hits [2]int
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: bad, onStream: func() { hits[0]++ }})
	p.Add("b", &fakeAd{name: "test", onStream: func() { hits[1]++ }})

	_, err := p.Stream(context.Background(), llm.RequestMessages{})
	if err == nil {
		t.Fatal("参数错误应直接失败")
	}
	if hits[0] != 1 || hits[1] != 0 {
		t.Fatalf("ClientFixable 错误不应换号: hits=%v", hits)
	}
	if p.HealthyCount() != 2 {
		t.Fatalf("参数错误不应冷却账号")
	}
}

// TestPoolAllCooling 验证全部账号冷却时的语义化错误。
func TestPoolAllCooling(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: rateLimitedErr("x")})
	p.Add("b", &fakeAd{name: "test", err: rateLimitedErr("y")})

	// 第一次请求会尝试两个账号后失败。
	_, err1 := p.Stream(context.Background(), llm.RequestMessages{})
	if err1 == nil {
		t.Fatal("全部限流时应失败")
	}
	// 两个账号都已冷却，第二次请求应立即得到 all_accounts_cooling。
	_, err2 := p.Stream(context.Background(), llm.RequestMessages{})
	f := llm.Wrap(err2)
	if f.Code != "all_accounts_cooling" {
		t.Fatalf("全冷却应返回 all_accounts_cooling, got %s", f.Code)
	}
}

// TestPoolEmpty 验证空号池的明确报错。
func TestPoolEmpty(t *testing.T) {
	p := NewPool("test", nil)
	_, err := p.Stream(context.Background(), llm.RequestMessages{})
	f := llm.Wrap(err)
	if f.Code != "no_account" {
		t.Fatalf("空池应返回 no_account, got %s", f.Code)
	}
}

// TestPoolTransportErrorDoesNotCooldown 验证传输错误换号但不冷却账号
// （可能是全局抖动，账号本身没问题）。
func TestPoolTransportErrorDoesNotCooldown(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: llm.NewFailure("upstream_unreachable", "connection refused", nil)})
	p.Add("b", &fakeAd{name: "test"})

	_, err := p.Stream(context.Background(), llm.RequestMessages{})
	if err != nil {
		t.Fatalf("传输错误应换号后成功: %v", err)
	}
	if p.HealthyCount() != 2 {
		t.Fatalf("传输错误不应冷却账号")
	}
}

// TestPoolStatusesAndDescribe 验证状态快照与 Describer 输出。
func TestPoolStatusesAndDescribe(t *testing.T) {
	p := NewPool("wb", nil)
	p.Add("a.json", &fakeAd{name: "wb", err: rateLimitedErr("x")})
	p.Add("b.json", &fakeAd{name: "wb"})
	_, _ = p.Stream(context.Background(), llm.RequestMessages{})

	sts := p.Statuses()
	if len(sts) != 2 {
		t.Fatalf("应有 2 条状态, got %d", len(sts))
	}
	// 模型级限流不改变账号健康：两个账号都应健康，Notes 不再罗列。
	d := p.Describe()
	if d.Status != "active" {
		t.Fatalf("账号本身健康时不应 degraded: %+v", d)
	}
}

// TestPoolRechecksCooldownEachHop 是「陈旧 now 快照」的回归测试：
// 换号链上前一个账号的拨号耗时可能超过后一账号的剩余冷却，
// 冷却判定必须每轮重取时间，否则刚解冻的账号会被错误跳过。
func TestPoolRechecksCooldownEachHop(t *testing.T) {
	// slow: 传输错误 + 1.5s 拨号；cooled: 剩约 500ms 冷却，之后正常服务。
	slow := &fakeAd{name: "test", err: llm.NewFailure("upstream_unreachable", "boom", nil),
		onStream: func() { time.Sleep(1200 * time.Millisecond) }}
	var cooledHits int
	cooled := &fakeAd{name: "test", onStream: func() { cooledHits++ }}

	p := NewPool("test", nil)
	p.Add("slow", slow)
	p.Add("cooled", cooled)
	// 手动把 cooled 置于「剩 500ms 冷却」状态（快于 slow 的拨号耗时）。
	p.mu.Lock()
	p.accounts[1].cooldownUntil = time.Now().Add(500 * time.Millisecond)
	p.mu.Unlock()

	start := time.Now()
	if _, err := p.Stream(context.Background(), llm.RequestMessages{}); err != nil {
		t.Fatalf("cooled 到期后应可服务: %v", err)
	}
	elapsed := time.Since(start)
	if cooledHits != 1 {
		t.Fatalf("换号链上已解冻的账号应被使用, cooledHits=%d", cooledHits)
	}
	if elapsed < 1200*time.Millisecond {
		t.Fatalf("应先经历 slow 的拨号耗时, elapsed=%v", elapsed)
	}
}

// TestPoolFairRotationWithCoolingAccount 是「轮询倾斜」的回归测试：
// 冷却账号的存在不能让它的后继承受双倍流量（会连锁限流）。
func TestPoolFairRotationWithCoolingAccount(t *testing.T) {
	var hits [3]int
	mk := func(i int) *fakeAd { return &fakeAd{name: "test", onStream: func() { hits[i]++ }} }
	p := NewPool("test", nil)
	p.Add("a", mk(0))
	p.Add("b", mk(1))
	p.Add("c", mk(2))

	// b 进入冷却（人为置冷却，绕过真实的限流触发）。
	p.mu.Lock()
	p.accounts[1].cooldownUntil = time.Now().Add(time.Hour)
	p.mu.Unlock()

	for i := 0; i < 30; i++ {
		if _, err := p.Stream(context.Background(), llm.RequestMessages{}); err != nil {
			t.Fatalf("请求不应失败: %v", err)
		}
	}
	if hits[0] != 15 || hits[2] != 15 {
		t.Fatalf("两个健康账号应均分流量, hits=%v（b 的冷却不得造成倾斜）", hits)
	}
}

// TestPoolAllCoolingReturns429AndRetryAfter 验证全冷却返回 429 语义并携带退避秒数：
// 客户端 SDK 对 429 会按限流节奏退避；502/server_error 会被当成服务器故障处理。
func TestPoolAllCoolingReturns429AndRetryAfter(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: rateLimitedErr("x")})
	p.Add("b", &fakeAd{name: "test", err: rateLimitedErr("y")})

	_, _ = p.Stream(context.Background(), llm.RequestMessages{}) // 触发双冷却
	_, err := p.Stream(context.Background(), llm.RequestMessages{})
	f := llm.Wrap(err)
	if f.Code != "all_accounts_cooling" {
		t.Fatalf("code=%s", f.Code)
	}
	if !f.RateLimited {
		t.Fatalf("全冷却应置 RateLimited（客户端需要 429 语义）: %+v", f)
	}
	if f.HTTPStatus() != 429 {
		t.Fatalf("HTTPStatus=%d, want 429", f.HTTPStatus())
	}
	if f.RetryAfterSeconds <= 0 {
		t.Fatalf("应携带最长剩余冷却秒数, got %d", f.RetryAfterSeconds)
	}
}

// ───────────────── 多账号加固（参考 sub2api / CLIProxyAPI 后新增） ─────────────────

// TestCooldownNeverShortens 是「并发冷却互相缩短」的回归测试（模型维度）。
// 同一 (账号, 模型) 上，后写者不得砍短已判定的冷却窗口。
func TestCooldownNeverShortens(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test"})
	acc := p.accounts[0]

	p.cooldownModel(acc, "m1", 60*time.Second, "long")
	first := acc.modelCooldown["m1"]

	p.cooldownModel(acc, "m1", 5*time.Second, "short")
	if acc.modelCooldown["m1"].Before(first) {
		t.Fatalf("冷却被缩短了：%v → %v", first, acc.modelCooldown["m1"])
	}
	// 更长的冷却必须能延长。
	p.cooldownModel(acc, "m1", 120*time.Second, "longer")
	if !acc.modelCooldown["m1"].After(first) {
		t.Fatalf("更长的冷却未生效：%v", acc.modelCooldown["m1"])
	}
	// 过期后必须能正常写入新冷却（防「只延长」把过期值锁死）。
	acc.modelCooldown["m1"] = time.Now().Add(-time.Second)
	p.cooldownModel(acc, "m1", 30*time.Second, "after-expiry")
	if !acc.modelCooldown["m1"].After(time.Now()) {
		t.Fatal("过期后新冷却未写入")
	}
	// 不同模型互不影响。
	if _, ok := acc.modelCooldown["m2"]; ok {
		t.Fatal("m1 的冷却不应波及 m2")
	}
}

// TestCooldownCapsAtMax 验证上限对所有路径生效（原实现的上限夹取是死代码）。
func TestCooldownCapsAtMax(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test"})
	acc := p.accounts[0]
	p.cooldownModel(acc, "m1", 999*time.Hour, "absurd")
	if d := time.Until(acc.modelCooldown["m1"]); d > maxCooldown+time.Minute {
		t.Fatalf("冷却未受上限约束: %v", d)
	}
}

// TestProbeFirstEventDetectsInBandError 是「HTTP 200 但流内首发即报错」的回归测试。
// 这是 HTTP 状态层拒绝与流内失败之间的真实缺口：没有首帧探针，这种失败
// 拿不到换号机会。
func TestProbeFirstEventDetectsInBandError(t *testing.T) {
	// 首帧即错误 → 探测失败，池应换号。
	// 注意生产侧必须显式置 RateLimited（httpError 就是这么做的）；
	// 只靠消息文本匹配不到 "quota" 这类词，分类会落空。
	bad := &fakeAd{name: "test", events: []llm.ResponseEvent{
		{Type: llm.EventError, Error: rateLimitedFailure("quota exceeded")},
	}}
	good := &fakeAd{name: "test", events: []llm.ResponseEvent{
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "ok"},
	}}
	p := NewPool("test", nil)
	p.Add("bad", bad)
	p.Add("good", good)

	s, err := p.Stream(context.Background(), llm.RequestMessages{Model: "model-x"})
	if err != nil {
		t.Fatalf("应换到健康账号: %v", err)
	}
	defer func() { _ = closeStream(s) }()
	// 限流是模型级的：该模型在 bad 账号上被冷却，账号本身仍健康。
	p.mu.Lock()
	cooling := p.accounts[0].modelCooling(time.Now(), "model-x")
	p.mu.Unlock()
	if !cooling {
		t.Fatal("首帧报错的模型应被冷却")
	}
	// 控制台可见：bad 账号的 ModelCooldowns 里有该模型。
	for _, st := range p.Statuses() {
		if st.Label == "bad" && st.ModelCooldowns["model-x"] <= 0 {
			t.Fatalf("应暴露该模型的冷却: %+v", st.ModelCooldowns)
		}
	}
}

// TestProbeFirstEventPreservesContent 是首帧探针的关键防回归用例：
// 探测读到的事件必须原样透传给上层，绝不能吞掉。
func TestProbeFirstEventPreservesContent(t *testing.T) {
	inner := &fakeAd{name: "test", events: []llm.ResponseEvent{
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "第一帧"},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "第二帧"},
	}}
	s, err := inner.Stream(context.Background(), llm.RequestMessages{})
	if err != nil {
		t.Fatal(err)
	}
	bs, err := probeFirstEvent(context.Background(), s)
	if err != nil {
		t.Fatalf("正常流不应探测失败: %v", err)
	}
	// 首帧必须还在。
	ev, err := bs.Recv(context.Background())
	if err != nil || ev.Delta != "第一帧" {
		t.Fatalf("首帧被吞: ev=%+v err=%v", ev, err)
	}
	ev2, err := bs.Recv(context.Background())
	if err != nil || ev2.Delta != "第二帧" {
		t.Fatalf("后续帧异常: ev=%+v err=%v", ev2, err)
	}
}

// TestAllBlockedReturnsUnauthorized 是「全池 401」的回归测试：
// 全部账号鉴权失效是终态失败，必须返回 401 而非 429——
// 返回 429 会让客户端无限重试一个永远不可能成功的请求。
func TestAllBlockedReturnsUnauthorized(t *testing.T) {
	unauth := llm.NewFailure("unauthorized", "token expired", nil)
	unauth.Unauthorized = true
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: unauth})
	p.Add("b", &fakeAd{name: "test", err: unauth})

	_, _ = p.Stream(context.Background(), llm.RequestMessages{}) // 触发双 blocked
	_, err := p.Stream(context.Background(), llm.RequestMessages{})
	f := llm.Wrap(err)
	if f.Code != "all_accounts_blocked" {
		t.Fatalf("code=%s, want all_accounts_blocked", f.Code)
	}
	if f.RateLimited {
		t.Fatal("全池鉴权失效不应标为 RateLimited")
	}
	if f.HTTPStatus() != 401 {
		t.Fatalf("HTTPStatus=%d, want 401", f.HTTPStatus())
	}
}

// TestAllCoolingUsesEarliestReset 验证全冷却时透传的是**最早**解冻秒数：
// 用最晚值会让客户端一直等到最后一个账号恢复，白等已有的可用容量。
func TestAllCoolingUsesEarliestReset(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", events: nil})
	p.Add("b", &fakeAd{name: "test", events: nil})
	p.mu.Lock()
	p.accounts[0].state = stateCooldown
	p.accounts[0].cooldownUntil = time.Now().Add(300 * time.Second) // 5 分钟
	p.accounts[1].state = stateCooldown
	p.accounts[1].cooldownUntil = time.Now().Add(30 * time.Second) // 30 秒
	p.mu.Unlock()

	_, err := p.Stream(context.Background(), llm.RequestMessages{})
	f := llm.Wrap(err)
	if !f.RateLimited || f.HTTPStatus() != 429 {
		t.Fatalf("全冷却应返回 429 语义: %+v", f)
	}
	if f.RetryAfterSeconds > 60 {
		t.Fatalf("应取最早解冻（≈30s），实际 %ds（取成了最晚）", f.RetryAfterSeconds)
	}
	if f.RetryAfterSeconds <= 0 {
		t.Fatalf("RetryAfterSeconds=%d，应为正数", f.RetryAfterSeconds)
	}
}

// TestRateLimitBackoffEscalates 验证无上游重置时刻时的指数退避与成功清零。
func TestRateLimitBackoffEscalates(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test"})
	acc := p.accounts[0]

	d1 := p.nextRateLimitBackoff(acc, "m1", llm.NewFailure("rate_limited", "x", nil))
	p.cooldownModel(acc, "m1", d1, "x")
	// 窗口未关：不升级。
	d2 := p.nextRateLimitBackoff(acc, "m1", llm.NewFailure("rate_limited", "x", nil))
	if d2 != d1 {
		t.Fatalf("冷却窗口未关时不应升级: %v → %v", d1, d2)
	}
	// 窗口过期后再失败：升级。
	acc.modelCooldown["m1"] = time.Now().Add(-time.Second)
	d3 := p.nextRateLimitBackoff(acc, "m1", llm.NewFailure("rate_limited", "x", nil))
	if d3 <= d1 {
		t.Fatalf("窗口过期后应升级: %v → %v", d1, d3)
	}
	// 上游给了精确时刻：完全信任，不动等级。
	before := acc.backoffLevel["m1"]
	withRA := llm.NewFailure("rate_limited", "x", nil)
	withRA.RetryAfterSeconds = 42
	if got := p.nextRateLimitBackoff(acc, "m1", withRA); got != 42*time.Second {
		t.Fatalf("应信任上游 Retry-After, got %v", got)
	}
	if acc.backoffLevel["m1"] != before {
		t.Fatal("上游给了精确时刻时不应推进退避等级")
	}
	// 成功清零。
	p.succeed(0)
	if len(acc.backoffLevel) != 0 {
		t.Fatalf("成功后应清零, got %+v", acc.backoffLevel)
	}
}

// TestStatusesExposeStateAndNext 验证状态快照含 state/reason/is_next
// （控制台号池可视化依赖这三个字段）。
func TestStatusesExposeStateAndNext(t *testing.T) {
	unauth := llm.NewFailure("unauthorized", "bad", nil)
	unauth.Unauthorized = true
	p := NewPool("test", nil)
	p.Add("a.json", &fakeAd{name: "test", err: rateLimitedErr("quota")})
	p.Add("b.json", &fakeAd{name: "test", err: unauth})
	p.Add("c.json", &fakeAd{name: "test"})

	_, _ = p.Stream(context.Background(), llm.RequestMessages{})
	sts := p.Statuses()
	if len(sts) != 3 {
		t.Fatalf("应有 3 条, got %d", len(sts))
	}
	byLabel := map[string]AccountStatus{}
	for _, st := range sts {
		byLabel[st.Label] = st
	}
	if !byLabel["a.json"].Healthy {
		t.Fatalf("单模型限流不应让账号不健康: %+v", byLabel["a.json"])
	}
	if byLabel["a.json"].ModelCooldowns["(unknown)"] <= 0 {
		t.Fatalf("应暴露被限模型的冷却: %+v", byLabel["a.json"].ModelCooldowns)
	}
	if byLabel["b.json"].State != "blocked" || byLabel["b.json"].Reason != "unauthorized" {
		t.Fatalf("鉴权失效账号状态错误: %+v", byLabel["b.json"])
	}
	if !byLabel["c.json"].Healthy {
		t.Fatalf("健康账号状态错误: %+v", byLabel["c.json"])
	}
	// 恰有一个 is_next。
	nextCount := 0
	for _, st := range sts {
		if st.IsNext {
			nextCount++
		}
	}
	if nextCount != 1 {
		t.Fatalf("is_next 应恰为 1 个, got %d", nextCount)
	}
}

// ───────────────── 模型级冷却（限流按「账号 × 模型」维度） ─────────────────

// TestRateLimitCooldownIsPerModel 是本次修复的核心回归：
// 一个模型被限流，不得把同账号上其他模型一起冷掉。
//
// 依据是上游自己的提示：「您的使用量已超出频率限制，将在 …重置，
// 您也可以切换其他模型继续使用」——限流是「账号 × 模型」维度的。
// 旧实现按账号冷却，会把该账号其他模型的可用额度白扔（实测可达数小时）。
func TestRateLimitCooldownIsPerModel(t *testing.T) {
	// 账号 a 只在 model-x 上被限；model-y 正常应能继续用 a。
	limited := map[string]bool{"model-x": true}
	var hitsA, hitsB int
	a := &fakeAd{name: "test", onStream: func() { hitsA++ }}
	a.limitModel = limited
	b := &fakeAd{name: "test", onStream: func() { hitsB++ }}

	p := NewPool("test", nil)
	p.Add("a", a)
	p.Add("b", b)

	// 第一次请求 model-x：a 被限 → 换到 b。
	if _, err := p.Stream(context.Background(), llm.RequestMessages{Model: "model-x"}); err != nil {
		t.Fatalf("model-x 应能换号成功: %v", err)
	}

	// 关键断言：a 上 model-x 已冷却，但 model-y 仍可用。
	p.mu.Lock()
	accA := p.accounts[0]
	now := time.Now()
	xCooling := accA.modelCooling(now, "model-x")
	yAvailable := accA.available(now, "model-y")
	acctHealthy := accA.available(now, "")
	p.mu.Unlock()

	if !xCooling {
		t.Fatal("model-x 应处于冷却中")
	}
	if !yAvailable {
		t.Fatal("model-y 不应受 model-x 限流影响（上游明确说可切换其他模型）")
	}
	if !acctHealthy {
		t.Fatal("账号不应因单模型限流而整体不可用")
	}
}

// TestModelCooldownDoesNotBlockOtherModelsInPool 端到端：
// model-x 被限后，model-x 请求走 b；model-y 请求仍可用 a（轮询）。
func TestModelCooldownDoesNotBlockOtherModelsInPool(t *testing.T) {
	a := &fakeAd{name: "test", limitModel: map[string]bool{"model-x": true}}
	b := &fakeAd{name: "test"}

	p := NewPool("test", nil)
	p.Add("a", a)
	p.Add("b", b)

	// 触发 a 上 model-x 的冷却。
	_, _ = p.Stream(context.Background(), llm.RequestMessages{Model: "model-x"})

	// model-y：a 应该仍然参与轮询（两个账号都可用）。
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		s, err := p.Stream(context.Background(), llm.RequestMessages{Model: "model-y"})
		if err != nil {
			t.Fatalf("model-y 不应失败: %v", err)
		}
		if l, ok := s.(interface{ AccountLabel() string }); ok {
			seen[l.AccountLabel()] = true
		}
		_ = closeStream(s)
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("model-y 应在两个账号间轮询，实际用到: %v", seen)
	}
}

// TestAllAccountsCoolingIsPerModel 验证全池冷却的错误消息指明是哪个模型。
func TestAllAccountsCoolingIsPerModel(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: rateLimitedErr("quota")})
	p.Add("b", &fakeAd{name: "test", err: rateLimitedErr("quota")})

	_, _ = p.Stream(context.Background(), llm.RequestMessages{Model: "model-x"})
	_, err := p.Stream(context.Background(), llm.RequestMessages{Model: "model-x"})
	f := llm.Wrap(err)
	if f.Code != "all_accounts_cooling" {
		t.Fatalf("code=%s", f.Code)
	}
	if !strings.Contains(f.Message, "model-x") {
		t.Fatalf("错误消息应指明被限的模型: %q", f.Message)
	}
}

// TestStatusesExposeModelCooldowns 验证控制台能拿到「哪些模型在冷却」。
func TestStatusesExposeModelCooldowns(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", limitModel: map[string]bool{"model-x": true}})
	p.Add("b", &fakeAd{name: "test"})
	_, _ = p.Stream(context.Background(), llm.RequestMessages{Model: "model-x"})

	for _, st := range p.Statuses() {
		if st.Label != "a" {
			continue
		}
		if st.ModelCooldowns["model-x"] <= 0 {
			t.Fatalf("a 应暴露 model-x 的冷却: %+v", st.ModelCooldowns)
		}
		if !st.Healthy {
			t.Fatal("账号本身应仍是健康的（只是某个模型被限）")
		}
	}
}
