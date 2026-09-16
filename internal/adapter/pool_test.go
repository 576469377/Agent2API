package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// fakeAd 是可编程假适配器：Stream 按预设返回错误或成功。
type fakeAd struct {
	name string
	err  error
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
	return &fakeStream{}, nil
}

type fakeStream struct{}

func (s *fakeStream) Recv(context.Context) (llm.ResponseEvent, error) {
	return llm.ResponseEvent{}, llm.ErrStreamDone
}

func (f *fakeAd) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }
func (f *fakeAd) Name() string                                    { return f.name }

func rateLimitedErr(msg string) error {
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
