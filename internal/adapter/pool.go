package adapter

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// 号池的冷却策略常量。
const (
	// cooldownRateLimitedDefault 是限流且上游未给出重置时间时的默认冷却时长。
	cooldownRateLimitedDefault = 60 * time.Second
	// cooldownUnauthorized 是刷新后仍 401 的账号冷却时长：
	// 多半是账号失效/权益异常，短时间重试没有意义。
	cooldownUnauthorized = 10 * time.Minute
	// maxCooldown 是任何冷却的上限，防止异常数据把账号永久挂起。
	maxCooldown = 24 * time.Hour
)

// Pool 把同一平台的 N 个适配器包装成一个 Adapter：按轮询调度健康账号，
// 失败时按错误类别决定「冷却该账号并换下一个」还是「直接失败」。
//
// 它只依赖 adapter.Adapter 接口与 llm.Failure 的分类字段，
// 不感知任何具体平台——因此对后续接入的新平台同样可用。
// 对 app 层而言它就是一个普通 Adapter，下游协议零改动（N+M 的 N 侧内部演化）。
type Pool struct {
	name string
	logf func(format string, args ...any)

	mu       sync.Mutex
	accounts []*poolAccount
	next     int // 轮询游标，保证请求在健康账号间均匀分布
}

type poolAccount struct {
	adp           Adapter
	label         string
	cooldownUntil time.Time
	lastErr       string
}

// AccountStatus 是单个账号的运行状态快照。
type AccountStatus struct {
	Label         string    `json:"label"`
	Healthy       bool      `json:"healthy"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
	CooldownSecs  int       `json:"cooldown_secs,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	Adapter       Adapter   `json:"-"`
}

// NewPool 创建号池。name 是平台标识（账号必须同平台，模型目录才可互换）。
// logf 可为 nil。
func NewPool(name string, logf func(format string, args ...any)) *Pool {
	return &Pool{name: name, logf: logf}
}

// Add 向号池注册一个账号。label 用于日志与控制台展示（如凭证文件名）。
func (p *Pool) Add(label string, adp Adapter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = append(p.accounts, &poolAccount{adp: adp, label: label})
}

// Len 返回账号总数。
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accounts)
}

// Name 实现 adapter.Adapter：返回平台标识。
func (p *Pool) Name() string { return p.name }

func (p *Pool) logfNow(format string, args ...any) {
	if p.logf != nil {
		p.logf(format, args...)
	}
}

// pick 返回下一个健康账号；全部冷却中时返回 nil。
// 调用方必须持有 p.mu。
func (p *Pool) pickLocked(now time.Time) *poolAccount {
	n := len(p.accounts)
	for i := 0; i < n; i++ {
		acc := p.accounts[(p.next+i)%n]
		if !acc.cooldownUntil.After(now) {
			return acc
		}
	}
	return nil
}

// Stream 实现 adapter.Adapter：在健康账号间轮询，失败按类别换号或直接失败。
//
// 换号只在「请求尚未产出任何内容」的 dial 阶段安全——这与单个适配器内部
// 重试的边界一致；流一旦建立、内容一旦下发，错误就走流内通道，绝不重发。
func (p *Pool) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	p.mu.Lock()
	n := len(p.accounts)
	if n == 0 {
		p.mu.Unlock()
		return nil, &llm.Failure{Code: "no_account", Message: "号池为空：未配置任何可用账号", ClientFixable: true}
	}
	start := p.next % n
	p.mu.Unlock()

	var lastErr error
	for i := 0; i < n; i++ {
		// 冷却判定每轮重取时间：换号链上前一个账号的拨号+退避可能耗时数秒，
		// 用入口快照会把「刚刚解冻」的账号错误跳过。
		now := time.Now()
		idx := (start + i) % n
		p.mu.Lock()
		acc := p.accounts[idx]
		cooling := acc.cooldownUntil.After(now)
		p.mu.Unlock()
		if cooling {
			continue
		}

		s, err := acc.adp.Stream(ctx, req)
		if err == nil {
			// 游标对齐到「实际服务的账号」之后：若只按槽位轮转再跳过冷却账号，
			// 冷却账号的后继会承受双倍流量，反而加速它触发限流（连锁冷却）。
			p.mu.Lock()
			p.next = (idx + 1) % n
			p.mu.Unlock()
			return s, nil
		}
		lastErr = err

		// 客户端已放弃：不再换号，原样返回。
		if ctx.Err() != nil {
			return nil, err
		}

		f := llm.Wrap(err)
		switch {
		case f.RateLimited:
			// 该账号额度受限：冷却它并换下一个账号。
			d := p.cooldownFor(f)
			p.cooldown(acc, d, f.Message)
			p.logfNow("账号 %s 触发限流，冷却 %s: %s", acc.label, d.Truncate(time.Second), f.Message)
		case f.Unauthorized:
			// 单个适配器内部已尝试过刷新重试；仍 401 说明账号大概率失效。
			p.cooldown(acc, cooldownUnauthorized, f.Message)
			p.logfNow("账号 %s 鉴权失败，冷却 %s: %s", acc.label, cooldownUnauthorized, f.Message)
		case f.ClientFixable:
			// 请求本身有问题（参数错、上下文超限等），换任何账号结果都一样。
			return nil, err
		default:
			// 传输断裂 / 5xx / 未知：可能是全局抖动，不冷却账号，换下一个试。
			p.logfNow("账号 %s 拨号失败，尝试下一个账号: %s", acc.label, f.Error())
		}

		// 换号前检查 ctx，避免上游已取消还继续打下一家。
		if ctx.Err() != nil {
			return nil, err
		}
	}

	if lastErr == nil {
		// 走到这里说明所有账号都在冷却中。这在语义上是「池级限流」：
		// 置 RateLimited 让客户端拿到 429 语义（SDK 会按限流节奏退避，
		// 而不是把 502 当服务器错误处理），并透传最长剩余冷却秒数。
		p.mu.Lock()
		maxRemaining := 0
		now := time.Now()
		for _, acc := range p.accounts {
			if s := int(acc.cooldownUntil.Sub(now).Seconds()); s > maxRemaining {
				maxRemaining = s
			}
		}
		p.mu.Unlock()
		return nil, &llm.Failure{
			Code:              "all_accounts_cooling",
			Message:           "所有账号都在冷却中，请稍后重试",
			RateLimited:       true,
			RetryAfterSeconds: maxRemaining,
			UpstreamFault:     true,
		}
	}
	return nil, lastErr
}

// cooldownFor 计算限流冷却时长：优先用上游给的 Retry-After / 重置时间
// （已由 workbuddy 层解析进 RetryAfterSeconds），否则用默认值。
func (p *Pool) cooldownFor(f *llm.Failure) time.Duration {
	if f.RetryAfterSeconds > 0 {
		d := time.Duration(f.RetryAfterSeconds) * time.Second
		if d > maxCooldown {
			d = maxCooldown
		}
		if d < time.Second {
			d = time.Second
		}
		return d
	}
	return cooldownRateLimitedDefault
}

func (p *Pool) cooldown(acc *poolAccount, d time.Duration, reason string) {
	until := time.Now().Add(d)
	if until.Sub(time.Now()) > maxCooldown {
		until = time.Now().Add(maxCooldown)
	}
	p.mu.Lock()
	acc.cooldownUntil = until
	acc.lastErr = reason
	p.mu.Unlock()
}

// ListModels 实现 adapter.Adapter：用第一个健康账号的模型目录。
// 同平台账号的目录应当一致；全冷却时退回第一个账号让其报出真实错误。
func (p *Pool) ListModels(ctx context.Context) ([]ModelInfo, error) {
	p.mu.Lock()
	acc := p.pickLocked(time.Now())
	if acc == nil && len(p.accounts) > 0 {
		acc = p.accounts[0]
	}
	p.mu.Unlock()
	if acc == nil {
		return nil, &llm.Failure{Code: "no_account", Message: "号池为空：未配置任何可用账号", ClientFixable: true}
	}
	return acc.adp.ListModels(ctx)
}

// Statuses 返回各账号的运行状态快照（供横幅与控制台）。
func (p *Pool) Statuses() []AccountStatus {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]AccountStatus, 0, len(p.accounts))
	for _, acc := range p.accounts {
		st := AccountStatus{Label: acc.label, Adapter: acc.adp}
		if acc.cooldownUntil.After(now) {
			st.CooldownUntil = acc.cooldownUntil
			st.CooldownSecs = int(acc.cooldownUntil.Sub(now).Seconds())
			st.LastError = acc.lastErr
		} else {
			st.Healthy = true
		}
		out = append(out, st)
	}
	return out
}

// HealthyCount 返回当前不在冷却中的账号数。
func (p *Pool) HealthyCount() int {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, acc := range p.accounts {
		if !acc.cooldownUntil.After(now) {
			n++
		}
	}
	return n
}

// Describe 实现 adapter.Describer（纯本地状态，不发网络请求）：
// 任一账号健康即为 active，全部冷却即为 degraded。
func (p *Pool) Describe() Description {
	sts := p.Statuses()
	healthy := 0
	notes := ""
	var cooling []string
	for _, st := range sts {
		if st.Healthy {
			healthy++
		} else {
			cooling = append(cooling, st.Label)
		}
	}
	if len(cooling) > 0 {
		sort.Strings(cooling)
		notes = "冷却中: " + joinLabels(cooling)
	}
	status := "active"
	if healthy == 0 && len(sts) > 0 {
		status = "degraded"
		notes = "全部账号冷却中 " + notes
	}
	return Description{
		ID:      p.name,
		Name:    p.name,
		BaseURL: "",
		Status:  status,
		Notes:   notes,
	}
}

func joinLabels(labels []string) string {
	out := ""
	for i, l := range labels {
		if i > 0 {
			out += "、"
		}
		out += l
	}
	return out
}

// SetSanitize 实现 adapter.Configurable：作用于所有账号。
func (p *Pool) SetSanitize(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, acc := range p.accounts {
		if c, ok := acc.adp.(Configurable); ok {
			c.SetSanitize(v)
		}
	}
}

// Sanitize 实现 adapter.Configurable：读第一个可配置账号的状态。
func (p *Pool) Sanitize() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, acc := range p.accounts {
		if c, ok := acc.adp.(Configurable); ok {
			return c.Sanitize()
		}
	}
	return false
}

// InvalidateModels 实现 adapter.Configurable：让所有账号的模型缓存失效。
func (p *Pool) InvalidateModels() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, acc := range p.accounts {
		if c, ok := acc.adp.(Configurable); ok {
			c.InvalidateModels()
		}
	}
}

// 编译期断言：Pool 满足平台接缝的全部接口。
var (
	_ Adapter      = (*Pool)(nil)
	_ Describer    = (*Pool)(nil)
	_ Configurable = (*Pool)(nil)
)
