package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// 号池的冷却策略常量。
const (
	// cooldownRateLimitedDefault 是限流且上游未给出重置时间时的退避基数。
	cooldownRateLimitedDefault = 60 * time.Second
	// cooldownRateLimitFloor 是限流冷却的下限，避免亚秒级反复震荡。
	cooldownRateLimitFloor = 10 * time.Second
	// cooldownUnauthorized 是刷新后仍 401 的账号冷却时长：
	// 多半是账号失效/权益异常，短时间重试没有意义。
	cooldownUnauthorized = 10 * time.Minute
	// maxCooldown 是任何冷却的上限，防止异常数据把账号永久挂起。
	maxCooldown = 24 * time.Hour
)

// accountState 是账号的调度状态。
//
// 与 cooldownUntil 是**正交**关系：state 说明「为什么不可用」，
// cooldownUntil 说明「什么时候可能恢复」。读时判定，过期即回 ready，
// 因此不需要任何后台定时任务来解冻（两个参考实现都是这个模式）。
type accountState uint8

const (
	// stateReady 可用。
	stateReady accountState = iota
	// stateCooldown 限流中，到点自动恢复。
	stateCooldown
	// stateBlocked 鉴权失效（刷新后仍 401），需人工介入。
	stateBlocked
)

func (s accountState) String() string {
	switch s {
	case stateCooldown:
		return "cooldown"
	case stateBlocked:
		return "blocked"
	default:
		return "ready"
	}
}

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
	state         accountState
	cooldownUntil time.Time
	// backoffLevel 是连续限流的退避等级（0 起），成功一次即清零。
	backoffLevel int
	lastErr      string
}

// 冷却原因，用于控制台展示与调度决策。
const (
	reasonRateLimited  = "rate_limited"
	reasonUnauthorized = "unauthorized"
)

// AccountStatus 是单个账号的运行状态快照。
//
// CooldownUntil 用 *time.Time 而非 time.Time：Go 的 omitempty 对 time.Time
// 无效（结构体永不"空"），健康账号会输出 "0001-01-01T00:00:00Z" 这种噪声。
type AccountStatus struct {
	Label         string     `json:"label"`
	Healthy       bool       `json:"healthy"`
	State         string     `json:"state"` // ready / cooldown / blocked
	Reason        string     `json:"reason,omitempty"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	CooldownSecs  int        `json:"cooldown_secs,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	// IsNext 表示严格轮询下「下一个请求将使用该账号」。
	IsNext  bool    `json:"is_next"`
	Adapter Adapter `json:"-"`
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

// pickLocked 返回下一个健康账号；全部冷却中时返回 nil。
// 调用方必须持有 p.mu。
func (p *Pool) pickLocked(now time.Time) *poolAccount {
	n := len(p.accounts)
	for i := 0; i < n; i++ {
		acc := p.accounts[(p.next+i)%n]
		if acc.available(now) {
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
		usable := acc.available(now)
		p.mu.Unlock()
		if !usable {
			continue
		}

		s, err := acc.adp.Stream(ctx, req)
		if err == nil {
			// 首帧探针：把「HTTP 200 但流内首发即报错」纳入换号窗口。
			// 这是 HTTP 状态层拒绝与流内失败之间的真实缺口——没有它，
			// 事后置流的账号无法被换掉（请求体已被上游消费，重放不安全）。
			bs, probeErr := probeFirstEvent(ctx, s)
			if probeErr == nil {
				p.succeed(idx)
				return bs, nil
			}
			_ = closeStream(s)
			err = probeErr
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
			// 上游给了精确重置时刻就完全信任它，不叠加退避等级；
			// 没给才用指数退避（并发失败经 cooldown 的「只延长」合并，
			// 不会一次冲高等级）。
			d := p.nextRateLimitBackoff(acc, f)
			p.cooldown(acc, d, reasonRateLimited, f.Message)
			p.logfNow("账号 %s 触发限流，冷却 %s（第 %d 级）: %s",
				acc.label, d.Truncate(time.Second), acc.backoffLevel, f.Message)
		case f.Unauthorized:
			// 单个适配器内部已尝试过刷新重试；仍 401 说明账号大概率失效。
			p.cooldown(acc, cooldownUnauthorized, reasonUnauthorized, f.Message)
			p.logfNow("账号 %s 鉴权失败，冷却 %s: %s", acc.label, cooldownUnauthorized, f.Message)
		case f.ClientFixable:
			// 请求本身有问题（参数错、上下文超限等），换任何账号结果都一样。
			return nil, err
		default:
			// 传输断裂 / 5xx / 未知：可能是全局抖动，不冷却账号，换下一个试。
			p.logfNow("账号 %s 拨号失败，尝试下一个账号: %s", acc.label, f.Error())
			// 池级短退避 + 抖动：上游整体故障时避免把 N 个账号连续无间隔打完
			//（那会被上游判定为压制重试）。可被 ctx 取消。
			if i < n-1 {
				if err := sleepCtx(ctx, ditherBackoff(i)); err != nil {
					return nil, err
				}
			}
		}

		// 换号前检查 ctx，避免上游已取消还继续打下一家。
		if ctx.Err() != nil {
			return nil, err
		}
	}

	if lastErr == nil {
		// 所有账号都在冷却中（无账号真正尝试过）。这里必须区分两种语义：
		//   · 全部限流 → 是池级限流，返回 429 + **最早**解冻秒数，客户端按节奏退避；
		//   · 全部鉴权失效 → 是终态失败，返回 401；返回 429 会让客户端
		//     无意义地反复重试一个永远不会成功的请求。
		p.mu.Lock()
		now := time.Now()
		blocked := 0
		earliest := time.Time{}
		for _, acc := range p.accounts {
			if acc.state == stateBlocked {
				blocked++
			}
			if acc.cooldownUntil.After(now) {
				if earliest.IsZero() || acc.cooldownUntil.Before(earliest) {
					earliest = acc.cooldownUntil
				}
			}
		}
		p.mu.Unlock()

		if blocked == len(p.accounts) {
			return nil, &llm.Failure{
				Code:         "all_accounts_blocked",
				Message:      "所有账号鉴权失效，请重新登录后再试",
				Unauthorized: true,
			}
		}
		remaining := 0
		if !earliest.IsZero() {
			if s := int(earliest.Sub(now).Seconds()); s > 0 {
				remaining = s
			}
		}
		return nil, &llm.Failure{
			Code:              "all_accounts_cooling",
			Message:           "所有账号都在冷却中，请稍后重试",
			RateLimited:       true,
			RetryAfterSeconds: remaining,
			UpstreamFault:     true,
		}
	}
	return nil, lastErr
}

// available 判断账号当前是否可服务（读时判定：冷却已过期即视为可用）。
func (a *poolAccount) available(now time.Time) bool {
	return !a.cooldownUntil.After(now)
}

// succeed 记录一次成功：游标对齐到「实际服务的账号」之后，并清零退避等级。
//
// 游标对齐的必要性：若只按槽位轮转再跳过冷却账号，冷却账号的后继承受
// 双倍流量，反而加速它触发限流（连锁冷却）。
func (p *Pool) succeed(idx int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next = (idx + 1) % len(p.accounts)
	p.accounts[idx].backoffLevel = 0
	p.accounts[idx].state = stateReady
}

// nextRateLimitBackoff 计算限流冷却时长并推进退避等级。
//
// 上游给了精确重置时刻 → 完全信任，不动等级（它比我们的猜测准得多）。
// 否则按等级指数退避，且只在「上一个冷却窗口已过期」时才升级——
// 否则并发的一批失败会把等级一次冲到顶。
func (p *Pool) nextRateLimitBackoff(acc *poolAccount, f *llm.Failure) time.Duration {
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
	// 冷却窗口未关：沿用「当前已生效的那一档」，不重算也不升级。
	// 重算会用到已递增的等级，等于窗口内仍在上台阶——那正是要防的抖动。
	p.mu.Lock()
	level := acc.backoffLevel
	until := acc.cooldownUntil
	p.mu.Unlock()
	if until.After(time.Now()) {
		return p.backoffDuration(level - 1)
	}
	d := p.backoffDuration(level)
	p.mu.Lock()
	if acc.backoffLevel == level && acc.backoffLevel < 16 {
		acc.backoffLevel++
	}
	p.mu.Unlock()
	return d
}

// backoffDuration 由等级换算时长，带上限与地板。
func (p *Pool) backoffDuration(level int) time.Duration {
	if level < 0 {
		level = 0
	}
	d := cooldownRateLimitedDefault << uint(level)
	if d > maxCooldown || d <= 0 { // <=0 防移位溢出
		d = maxCooldown
	}
	if d < cooldownRateLimitFloor {
		d = cooldownRateLimitFloor
	}
	return d
}

// ditherBackoff 给换号之间的等待加抖动，避免多个并发请求同步重试。
func ditherBackoff(i int) time.Duration {
	base := 50 * time.Millisecond * time.Duration(i+1)
	if base > 500*time.Millisecond {
		base = 500 * time.Millisecond
	}
	// 抖动幅度 25%，用当前纳秒做种子，避免引入 rand 依赖与全局锁。
	jitter := time.Duration(time.Now().UnixNano()%int64(base/4+1)) * time.Nanosecond
	return base + jitter
}

// sleepCtx 可被 ctx 取消的等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// cooldown 写入冷却状态与原因。
//
// 关键不变量：**只延长、不缩短**。并发请求可能各自判定出不同时长
// （一个算出 60s、另一个 5s），若无条件覆盖，后写者会把已判定的
// 限流窗口砍短，等于白丢一次判定。参考两个成熟实现都是这个语义。
func (p *Pool) cooldown(acc *poolAccount, d time.Duration, reason, msg string) {
	if d <= 0 {
		return
	}
	if d > maxCooldown {
		d = maxCooldown
	}
	now := time.Now()
	until := now.Add(d)

	p.mu.Lock()
	defer p.mu.Unlock()
	// 已有更晚的存活冷却 → 不缩短（过期的不算，否则会永久锁死）。
	if acc.cooldownUntil.After(now) && acc.cooldownUntil.After(until) {
		return
	}
	acc.cooldownUntil = until
	acc.lastErr = msg
	if reason == reasonUnauthorized {
		acc.state = stateBlocked
	} else {
		acc.state = stateCooldown
	}
}

// probeFirstEvent 读取流的第一个事件，把「HTTP 200 但流内首发即失败」
// 纳入换号窗口；读到非错误事件则原样缓存并透传给上层。
//
// 必须缓存而非丢弃：已读到的事件属于响应内容，探测不能吞数据。
func probeFirstEvent(ctx context.Context, s llm.ResponseStream) (llm.ResponseStream, error) {
	ev, err := s.Recv(ctx)
	if err != nil {
		if errors.Is(err, llm.ErrStreamDone) {
			// 空流：不算失败，交给上层按正常结束处理。
			return &bufferedStream{inner: s}, nil
		}
		return nil, err
	}
	if ev.Type == llm.EventError {
		if ev.Error != nil {
			return nil, ev.Error
		}
		return nil, &llm.Failure{Code: "upstream_error", Message: "上游返回错误", UpstreamFault: true}
	}
	return &bufferedStream{inner: s, first: &ev}, nil
}

// bufferedStream 把探测阶段读到的事件缓存起来，先吐缓存再直通内层流。
type bufferedStream struct {
	inner llm.ResponseStream
	first *llm.ResponseEvent
}

func (b *bufferedStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	if b.first != nil {
		ev := *b.first
		b.first = nil
		return ev, nil
	}
	return b.inner.Recv(ctx)
}

// Close 透传内层流的关闭能力（上游连接释放依赖它）。
func (b *bufferedStream) Close() error { return closeStream(b.inner) }

func closeStream(s llm.ResponseStream) error {
	if c, ok := s.(io.Closer); ok {
		return c.Close()
	}
	return nil
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
	for i, acc := range p.accounts {
		st := AccountStatus{Label: acc.label, Adapter: acc.adp, IsNext: i == p.next%len(p.accounts)}
		if acc.available(now) {
			st.Healthy = true
			st.State = stateReady.String()
		} else {
			st.State = acc.state.String()
			until := acc.cooldownUntil
			st.CooldownUntil = &until
			st.CooldownSecs = int(acc.cooldownUntil.Sub(now).Seconds())
			st.LastError = acc.lastErr
			if acc.state == stateBlocked {
				st.Reason = reasonUnauthorized
			} else {
				st.Reason = reasonRateLimited
			}
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

	// 平台级信息（上游地址、账号名、模型数）从账号适配器借。
	// 之前这里留空，导致号池模式下平台卡显示「上游 -」「账号 -」——
	// 用户看到的平台信息残缺，而数据其实就在下面每个账号里。
	out := Description{ID: p.name, Name: p.name, Status: status, Notes: notes}
	if len(sts) > 0 {
		out.Models = 0
		if d, ok := sts[0].Adapter.(Describer); ok {
			inner := d.Describe()
			out.BaseURL = inner.BaseURL
			out.Account = inner.Account
			out.Models = inner.Models
		}
		if out.Account == "" {
			// 号池模式下「账号」更有意义的是账号数量。
			out.Account = fmt.Sprintf("%d 个账号", len(sts))
		}
	}
	return out
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
