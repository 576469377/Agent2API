package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// fakeAd 是可编程假适配器：Stream 按预设返回错误或按序回放事件。
type fakeAd struct {
	name string
	err  error
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
	if got := p.HealthyCount(); got != 1 {
		t.Fatalf("健康账号应剩 1 个, got %d", got)
	}
}

// TestPoolCooldownExpires 验证冷却到期后账号自动回到轮询。
func TestPoolCooldownExpires(t *testing.T) {
	var hits int
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test", err: rateLimitedErr("x"), onStream: func() { hits++ }})
	p.Add("b", &fakeAd{name: "test"})

	// 触发 a 的冷却。
	_, _ = p.Stream(context.Background(), llm.RequestMessages{})
	if p.HealthyCount() != 1 {
		t.Fatalf("限流后应只剩 1 个健康账号")
	}
	// 人为把冷却时间拨回过去，模拟到期。
	p.mu.Lock()
	p.accounts[0].cooldownUntil = time.Now().Add(-time.Second)
	p.mu.Unlock()
	if p.HealthyCount() != 2 {
		t.Fatalf("冷却到期后应恢复 2 个健康账号")
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
	d := p.Describe()
	if d.Status != "active" {
		t.Fatalf("还有健康账号时不应 degraded: %+v", d)
	}
	if d.Notes == "" {
		t.Fatal("有冷却账号时 Notes 应列出")
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

// TestCooldownNeverShortens 是「并发冷却互相缩短」的回归测试。
// 两个并发请求各自判定出不同时长时，后写者不得砍短已判定的窗口。
func TestCooldownNeverShortens(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test"})
	acc := p.accounts[0]

	p.cooldown(acc, 60*time.Second, reasonRateLimited, "long")
	first := acc.cooldownUntil

	p.cooldown(acc, 5*time.Second, reasonRateLimited, "short")
	if acc.cooldownUntil.Before(first) {
		t.Fatalf("冷却被缩短了：%v → %v", first, acc.cooldownUntil)
	}
	// 更长的冷却必须能延长。
	p.cooldown(acc, 120*time.Second, reasonRateLimited, "longer")
	if !acc.cooldownUntil.After(first) {
		t.Fatalf("更长的冷却未生效：%v", acc.cooldownUntil)
	}
	// 过期后必须能正常写入新冷却（防「只延长」把过期值锁死）。
	acc.cooldownUntil = time.Now().Add(-time.Second)
	p.cooldown(acc, 30*time.Second, reasonRateLimited, "after-expiry")
	if !acc.cooldownUntil.After(time.Now()) {
		t.Fatal("过期后新冷却未写入")
	}
}

// TestCooldownCapsAtMax 验证上限对所有路径生效（原实现的上限夹取是死代码）。
func TestCooldownCapsAtMax(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test"})
	acc := p.accounts[0]
	p.cooldown(acc, 999*time.Hour, reasonRateLimited, "absurd")
	if d := time.Until(acc.cooldownUntil); d > maxCooldown+time.Minute {
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

	s, err := p.Stream(context.Background(), llm.RequestMessages{})
	if err != nil {
		t.Fatalf("应换到健康账号: %v", err)
	}
	defer func() { _ = closeStream(s) }()
	// 限流账号被冷却 → 只剩 1 个健康账号。
	if got := p.HealthyCount(); got != 1 {
		t.Fatalf("限流账号应被冷却, healthy=%d（want 1）", got)
	}
	// 被冷却的账号应带上限流原因，供控制台展示。
	for _, st := range p.Statuses() {
		if st.Label == "bad" {
			if st.State != "cooldown" || st.Reason != "rate_limited" {
				t.Fatalf("冷却状态/原因不符: %+v", st)
			}
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

	d1 := p.nextRateLimitBackoff(acc, llm.NewFailure("rate_limited", "x", nil))
	p.cooldown(acc, d1, reasonRateLimited, "x")
	// 窗口未关：不升级。
	d2 := p.nextRateLimitBackoff(acc, llm.NewFailure("rate_limited", "x", nil))
	if d2 != d1 {
		t.Fatalf("冷却窗口未关时不应升级: %v → %v", d1, d2)
	}
	// 窗口过期后再失败：升级。
	acc.cooldownUntil = time.Now().Add(-time.Second)
	d3 := p.nextRateLimitBackoff(acc, llm.NewFailure("rate_limited", "x", nil))
	if d3 <= d1 {
		t.Fatalf("窗口过期后应升级: %v → %v", d1, d3)
	}
	// 上游给了精确时刻：完全信任，不动等级。
	before := acc.backoffLevel
	withRA := llm.NewFailure("rate_limited", "x", nil)
	withRA.RetryAfterSeconds = 42
	if got := p.nextRateLimitBackoff(acc, withRA); got != 42*time.Second {
		t.Fatalf("应信任上游 Retry-After, got %v", got)
	}
	if acc.backoffLevel != before {
		t.Fatal("上游给了精确时刻时不应推进退避等级")
	}
	// 成功清零。
	p.succeed(0)
	if acc.backoffLevel != 0 {
		t.Fatalf("成功后应清零, got %d", acc.backoffLevel)
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
	if byLabel["a.json"].State != "cooldown" || byLabel["a.json"].Reason != "rate_limited" {
		t.Fatalf("限流账号状态错误: %+v", byLabel["a.json"])
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
