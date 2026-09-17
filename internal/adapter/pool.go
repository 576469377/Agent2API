package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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

	// ewmaAlpha 是健康度指标的更新权重：越大越看重最近的表现。
	// 0.3 意味着「最近约 3 次」主导判断——既能快速反映劣化，
	// 又不会因单次抖动就把账号判死。
	ewmaAlpha = 0.3
	// healthWarmup 是冷启动样本数：样本不足时不参与排序比较，
	// 避免「只跑过一次就定终身」。
	healthWarmup = 3
	// latencyCeilingMs 是延迟评分的饱和值：超过它一律视为最慢。
	latencyCeilingMs = 60_000.0
	// consecFailPenalty 是每次连续失败扣的分（相对 0..1 的成功率分）。
	consecFailPenalty = 0.15
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
	// stateDisabled 由操作者在控制台手动停用（不参与调度，直到重新启用）。
	// 与 blocked 的区别：blocked 是上游判定，disabled 是人的决定。
	stateDisabled
)

func (s accountState) String() string {
	switch s {
	case stateCooldown:
		return "cooldown"
	case stateBlocked:
		return "blocked"
	case stateDisabled:
		return "disabled"
	default:
		return "ready"
	}
}

// Pool 把同一平台的 N 个适配器包装成一个 Adapter：按健康度调度健康账号，
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
	next     int // 同分裁决游标（健康度排序后的均分手段）
	// affinity 是会话亲和表：同一会话粘住同一账号（避免长上下文在账号间
	// 漂移导致上游重复处理全部输入）。
	affinity *affinityTable
}

type poolAccount struct {
	adp           Adapter
	label         string
	state         accountState
	cooldownUntil time.Time
	// modelCooldown 是**按模型**的限流冷却表。
	//
	// 上游限流是「账号 × 模型」维度的，不是账号维度：上游自己的提示就是
	// 「您也可以切换其他模型继续使用」。若整个账号一起冷却，会把该账号上
	// 其他仍然可用的模型额度一起浪费掉（实测能白白锁掉数小时）。
	// 键是模型 ID，值是解冻时刻。
	modelCooldown map[string]time.Time
	// backoffLevel 是按模型的连续限流退避等级（0 起），该模型成功一次即清零。
	backoffLevel map[string]int
	lastErr      string

	// —— 健康度（用于选号，见 pickLocked）——
	//
	// 严格轮询的问题：谁被选中与谁更该被选中无关。刚出过问题的账号
	// 照样轮到，快慢差异也不被利用。这里维护三个轻量指标，
	// 选号时按分数排序，轮询仅作为同分时的均分手段。
	successEWMA float64 // 成功率指数加权移动平均（0..1）
	latencyEWMA float64 // 延迟 EWMA（毫秒）
	samples     int64   // 累计样本数（用于冷启动判断）
	consecFails int     // 连续失败次数（成功即清零）
}

// modelKey 归一化模型名作为冷却键。空模型名不该出现，但防御性地给一个键，
// 避免 nil map 写入导致 panic。
func modelKey(model string) string {
	if model == "" {
		return "(unknown)"
	}
	return model
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
	// ModelCooldowns 是该账号上仍在冷却中的模型（模型 ID → 剩余秒数）。
	// 上游限流按模型维度，因此账号「健康」与「某些模型被限」可以并存。
	ModelCooldowns map[string]int `json:"model_cooldowns,omitempty"`
	// IsNext 表示严格轮询下「下一个请求将使用该账号」。
	IsNext  bool    `json:"is_next"`
	Adapter Adapter `json:"-"`
}

// NewPool 创建号池。name 是平台标识（账号必须同平台，模型目录才可互换）。
// logf 可为 nil。
func NewPool(name string, logf func(string, ...any)) *Pool {
	return &Pool{name: name, logf: logf, affinity: newAffinityTable()}
}

// Add 向号池注册一个账号。label 用于日志与控制台展示（如凭证文件名）。
func (p *Pool) Add(label string, adp Adapter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = append(p.accounts, &poolAccount{
		adp: adp, label: label,
		modelCooldown: map[string]time.Time{},
		backoffLevel:  map[string]int{},
	})
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

// healthScore 返回账号的健康度评分（0..1，越高越好）。
//
// 三个因子合成：
//   - 成功率 EWMA（权重最大，占 0.6）
//   - 延迟 EWMA（越快分越高，占 0.4）
//   - 连续失败惩罚（每次 -0.15）
//
// 冷启动（样本不足 healthWarmup）返回中性分 0.5。注意这个中性分与
// 「全成功」的分数（1.0）有明显的距离，所以冷启动期不会偏向谁；
// 但样本刚满 warmup 时分数会立刻跳到真实水平——为避免因此独占流量，
// pickByHealthLocked 用**加权随机**而非取最大值。
//
// 调用方必须持有 p.mu。
func (a *poolAccount) healthScore() float64 {
	// 原始健康分。
	lat := 1.0
	if a.latencyEWMA > 0 {
		lat = 1.0 - (a.latencyEWMA / latencyCeilingMs)
		if lat < 0 {
			lat = 0
		}
	}
	raw := a.successEWMA*0.6 + lat*0.4
	raw -= float64(a.consecFails) * consecFailPenalty
	if raw < 0 {
		raw = 0
	}
	if raw > 1 {
		raw = 1
	}
	// 向中性分 0.5 收缩：样本越少，越接近中性。
	//
	// 为什么不用硬阈值（samples < N 就返回 0.5）：那会在跨过阈值时产生阶跃，
	// 一个刚攒够样本的账号分数会突然跳高并锁死流量（实测 hits=[1 5]）。
	// 连续收缩让「证据强度」平滑反映到分数上：1 个样本只能推动很少，
	// 20 个样本才基本等同原始分。
	const prior = 0.5
	const priorWeight = 8.0 // 相当于「8 个中性样本」的先验
	w := float64(a.samples)
	return (raw*w + prior*priorWeight) / (w + priorWeight)
}

// pickLocked 按健康度选下一个账号。
//
// 取代严格轮询：先把可用账号按健康度排序，取分最高的；分数相同（含全部
// 冷启动的中性分）时按游标做均分——这样「同样健康」的账号仍被公平使用，
// 而「明显更差」的账号会被自然地降级。
//
// 轮询游标保留为同分裁决手段，不再是主策略。
// 调用方必须持有 p.mu。
func (p *Pool) pickLocked(now time.Time, model string) *poolAccount {
	n := len(p.accounts)
	if n == 0 {
		return nil
	}
	// 收集可用账号及其下标（下标用于同分时的轮询裁决）。
	type cand struct {
		acc   *poolAccount
		idx   int
		score float64
	}
	cands := make([]cand, 0, n)
	for i := 0; i < n; i++ {
		acc := p.accounts[i]
		if acc.available(now, model) {
			cands = append(cands, cand{acc: acc, idx: i, score: acc.healthScore()})
		}
	}
	if len(cands) == 0 {
		return nil
	}
	// 选出最高分；同分时取「轮询游标之后最先遇到的那个」，保持均分。
	best := 0
	for i := 1; i < len(cands); i++ {
		if cands[i].score > cands[best].score+1e-9 {
			best = i
		}
	}
	// 同分集合里按游标裁决。
	bestScore := cands[best].score
	start := p.next % n
	chosen := -1
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		acc := p.accounts[idx]
		if !acc.available(now, model) {
			continue
		}
		if acc.healthScore() >= bestScore-1e-9 {
			chosen = idx
			break
		}
	}
	if chosen < 0 {
		chosen = cands[best].idx
	}
	return p.accounts[chosen]
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
	p.mu.Unlock()

	// 会话亲和：同一会话优先粘住上次的账号（长上下文在账号间漂移会让
	// 上游重复处理全部输入）。
	//
	// 亲和失败（账号没了/首帧报错）时解绑并落入下方的正常换号循环，
	// 错误作为 lastErr 起点参与后续分类。
	var sk string
	var lastErr error
	tried := map[int]bool{}
	if req.SessionKey != "" {
		sk = req.SessionKey
		// 亲和查找必须在 p.mu 内完成：accounts 切片会被 poolwatch 的 Add
		// 并发 append（扩容重分配期间锁外读可撕裂），modelCooldown 会被
		// 限流路径并发写（map 并发读写是不可恢复的 fatal panic）。
		p.mu.Lock()
		bound, has := p.affinity.get(sk)
		var boundIdx = -1
		var boundAcc *poolAccount
		if has {
			for idx, acc := range p.accounts {
				if acc.label == bound && acc.available(time.Now(), req.Model) {
					boundIdx, boundAcc = idx, acc
					break
				}
			}
			if boundIdx < 0 {
				// 绑定的账号不在池里或不可用：解绑走正常调度。
				p.affinity.delete(sk)
			}
		}
		p.mu.Unlock()

		if boundAcc != nil {
			tried[boundIdx] = true
			s, err := boundAcc.adp.Stream(ctx, req)
			if err == nil {
				bs, probeErr := probeFirstEvent(ctx, s)
				if probeErr == nil {
					return &labeledStream{ResponseStream: bs, label: boundAcc.label}, nil
				}
				_ = closeStream(s)
				err = probeErr
			}
			// 亲和账号失败：解绑（持锁——affinity 表是裸 map），错误带入
			// 正常换号循环。
			lastErr = err
			p.mu.Lock()
			p.affinity.delete(sk)
			p.mu.Unlock()
		}
	}

	// 正常换号循环（亲和失败或无绑定时进入）。tried 已含亲和尝试过的账号。
	for len(tried) < n {

		now := time.Now()
		p.mu.Lock()
		idx, acc := p.pickByHealthLocked(now, req.Model, tried)
		p.mu.Unlock()
		if acc == nil {
			break // 没有未试过且可用的账号了
		}
		tried[idx] = true

		s, err := acc.adp.Stream(ctx, req)
		if err == nil {
			// 首帧探针：把「HTTP 200 但流内首发即报错」纳入换号窗口。
			// 这是 HTTP 状态层拒绝与流内失败之间的真实缺口——没有它，
			// 事后置流的账号无法被换掉（请求体已被上游消费，重放不安全）。
			bs, probeErr := probeFirstEvent(ctx, s)
			if probeErr == nil {
				p.succeed(idx, float64(time.Since(now).Milliseconds()))
				// 会话亲和回写：本次会话下次还来这个账号。
				if sk != "" {
					p.mu.Lock()
					p.affinity.set(sk, acc.label)
					p.mu.Unlock()
				}
				// 带上账号标签：app 层据此把请求归因到具体账号，
				// 控制台才能回答「每个账号用了多少额度」。
				return &labeledStream{ResponseStream: bs, label: acc.label}, nil
			}
			_ = closeStream(s)
			err = probeErr
		}
		lastErr = err
		// 会话亲和解绑：绑定的账号失败了，旧绑定已无意义
		//（下次请求会按健康度重新选择并重建绑定）。
		if sk != "" {
			p.mu.Lock()
			if bound, ok := p.affinity.get(sk); ok && bound == acc.label {
				p.affinity.delete(sk)
			}
			p.mu.Unlock()
		}

		// 客户端已放弃：不再换号，原样返回。
		if ctx.Err() != nil {
			return nil, err
		}

		f := llm.Wrap(err)
		switch {
		case f.RateLimited:
			// 限流是「账号 × 模型」维度的，只冷却该模型——同账号上其他模型
			// 仍然可用（上游提示原话：「您也可以切换其他模型继续使用」）。
			p.markFailure(acc)
			// 上游给了精确重置时刻就完全信任它，不叠加退避等级；
			// 没给才用指数退避。
			d, level := p.nextRateLimitBackoff(acc, req.Model, f)
			p.cooldownModel(acc, req.Model, d, f.Message)
			p.logfNow("账号 %s 的模型 %s 触发限流，冷却 %s（第 %d 级）: %s",
				acc.label, req.Model, d.Truncate(time.Second), level, f.Message)
		case f.Unauthorized:
			// 单个适配器内部已尝试过刷新重试；仍 401 说明账号大概率失效。
			// 这是**账号级**终态（与模型无关）。
			p.markFailure(acc)
			p.cooldownAccount(acc, cooldownUnauthorized, reasonUnauthorized, f.Message)
			p.logfNow("账号 %s 鉴权失败，冷却 %s: %s", acc.label, cooldownUnauthorized, f.Message)
		case f.ClientFixable:
			// 请求本身有问题（参数错、上下文超限等），换任何账号结果都一样。
			return nil, err
		default:
			// 传输断裂 / 5xx / 未知：可能是全局抖动，不冷却账号但降健康度
			//（下次选号会优先避开它），换下一个试。
			p.markFailure(acc)
			p.logfNow("账号 %s 拨号失败，尝试下一个账号: %s", acc.label, f.Error())
			// 池级短退避 + 抖动：上游整体故障时避免把 N 个账号连续无间隔打完
			//（那会被上游判定为压制重试）。可被 ctx 取消。
			if len(tried) < n {
				if err := sleepCtx(ctx, ditherBackoff(len(tried))); err != nil {
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
			// 该模型在此账号上最早何时解冻（账号级 + 模型级取较早者）。
			if t := acc.earliestModelThaw(now); !t.IsZero() {
				if earliest.IsZero() || t.Before(earliest) {
					earliest = t
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
			Message:           fmt.Sprintf("模型 %s 在所有账号上都被限流，请稍后重试（或改用其他模型）", req.Model),
			RateLimited:       true,
			RetryAfterSeconds: remaining,
			UpstreamFault:     true,
		}
	}
	return nil, lastErr
}

// accountUsable 判断账号**本身**是否可用（与具体模型无关）。
//
// 用于「健康账号数」「控制台账号健康」这类账号级语义：
// 某模型被限流不代表账号不健康——同账号上其他模型仍可服务。
// 只有停用 / 鉴权失效 / 账号级冷却才算整号不可用。
func (a *poolAccount) accountUsable(now time.Time) bool {
	if a.state == stateDisabled || a.state == stateBlocked {
		return false
	}
	return !a.cooldownUntil.After(now)
}

// available 判断账号能否服务指定模型。
//
// 两级判定：
//  1. 账号级——已停用 / 鉴权失效（blocked）则整号不可用；
//  2. 模型级——该模型若在限流冷却中，仅该模型不可用，账号上其他模型照常服务。
//
// 都是读时判定：冷却过期即视为可用，不需要后台解冻任务。
func (a *poolAccount) available(now time.Time, model string) bool {
	if a.state == stateDisabled || a.state == stateBlocked {
		return false
	}
	if !a.cooldownUntil.After(now) {
		// 账号级冷却已过期（历史遗留字段，用于无模型信息的退化冷却）。
		return !a.modelCooling(now, model)
	}
	return false
}

// modelCooling 判断某模型是否仍在冷却中。
func (a *poolAccount) modelCooling(now time.Time, model string) bool {
	until, ok := a.modelCooldown[modelKey(model)]
	return ok && until.After(now)
}

// earliestModelThaw 返回该账号上最早解冻的模型时刻（供全池冷却时估算等待）。
func (a *poolAccount) earliestModelThaw(now time.Time) time.Time {
	earliest := a.cooldownUntil
	for _, until := range a.modelCooldown {
		if !until.After(now) {
			continue
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	if !earliest.After(now) {
		return time.Time{}
	}
	return earliest
}

// succeed 记录一次成功：游标对齐到「实际服务的账号」之后，并清零退避等级。
//
// 游标对齐的必要性：若只按槽位轮转再跳过冷却账号，冷却账号的后继承受
// 双倍流量，反而加速它触发限流（连锁冷却）。
func (p *Pool) succeed(idx int, latencyMs float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[idx]
	p.next = (idx + 1) % len(p.accounts)
	// 成功一次即清零该账号所有模型的退避等级（整号可达已被证实）。
	a.backoffLevel = map[string]int{}
	if a.state == stateCooldown {
		a.state = stateReady
	}
	// 健康度更新：成功率走高、延迟 EWMA 跟上。
	a.observe(true, latencyMs)
}

// observe 用一次请求结果更新健康度 EWMA。
// 调用方必须持有 p.mu（或确保独占）。
func (a *poolAccount) observe(ok bool, latencyMs float64) {
	// 首样本从中性分 0.5 出发而非 1.0：直接落满分会让「第一个成功的账号」
	// 立刻获得压倒性优势，形成正反馈把流量锁死在它身上（实测 hits=[5 1]）。
	target := 0.0
	if ok {
		target = 1.0
	}
	if a.samples == 0 {
		a.successEWMA = 0.5
		a.latencyEWMA = latencyMs
	} else {
		a.successEWMA = a.successEWMA*(1-ewmaAlpha) + target*ewmaAlpha
		if latencyMs > 0 {
			a.latencyEWMA = a.latencyEWMA*(1-ewmaAlpha) + latencyMs*ewmaAlpha
		}
	}
	a.samples++
	if ok {
		a.consecFails = 0
	} else {
		a.consecFails++
	}
}

// markFailure 记一次失败（用于健康度降级）。调用方持有 p.mu。
func (a *poolAccount) markFailure() {
	a.observe(false, 0)
}

// pickByHealthLocked 在「未试过」的账号里按健康度**加权随机**选一个。
//
// 为什么不是「取最高分」：那会让分数略高的账号独占全部流量（哪怕只是
// 0.5001 vs 0.5），健康度路由退化成「永远用最好的那个」——被冷落的账号
// 永远拿不到新样本，健康度无从更新，也无法分摊限流风险。
//
// 加权随机既利用了健康差异（分高者更容易被选中），又保证每个账号都有
// 机会被验证与分摊。冷启动时大家同分，退化为均匀轮询——正是期望行为。
//
// 随机源用当前纳秒，避免引入 rand 全局锁；选号本就不要求密码学强度。
// 调用方必须持有 p.mu。
func (p *Pool) pickByHealthLocked(now time.Time, model string, tried map[int]bool) (int, *poolAccount) {
	n := len(p.accounts)
	if n == 0 {
		return -1, nil
	}
	// 收集候选与权重（权重下限保证「很差但可用」的账号仍有机会）。
	type cand struct {
		idx    int
		acc    *poolAccount
		weight float64
	}
	cands := make([]cand, 0, n)
	total := 0.0
	for i, acc := range p.accounts {
		if tried[i] || !acc.available(now, model) {
			continue
		}
		w := acc.healthScore()
		if w < 0.05 {
			w = 0.05 // 下限：不被完全饿死，否则永远拿不到新样本
		}
		cands = append(cands, cand{idx: i, acc: acc, weight: w})
		total += w
	}
	if len(cands) == 0 {
		return -1, nil
	}
	// 加权随机。
	//
	// 随机源刻意用 math/rand 而非 time.Now().UnixNano()：后者在 macOS 上
	// 精度约 1 微秒，同一微秒内的连续调用会读到完全相同的值——高并发下
	// 等于确定性选择，所有请求砸向同一个账号（实测 hits=[0 6]）。
	r := rand.Float64() * total
	acc := 0.0
	for _, c := range cands {
		acc += c.weight
		if r <= acc {
			return c.idx, c.acc
		}
	}
	return cands[len(cands)-1].idx, cands[len(cands)-1].acc
}

// nextRateLimitBackoff 计算某账号上某模型的限流冷却时长并推进其退避等级。
//
// 上游给了精确重置时刻 → 完全信任，不动等级（它比我们的猜测准得多）。
// 否则按等级指数退避，且只在「该模型的上一个冷却窗口已过期」时才升级——
// 否则并发的一批失败会把等级一次冲到顶。
func (p *Pool) nextRateLimitBackoff(acc *poolAccount, model string, f *llm.Failure) (time.Duration, int) {
	if f.RetryAfterSeconds > 0 {
		d := time.Duration(f.RetryAfterSeconds) * time.Second
		if d > maxCooldown {
			d = maxCooldown
		}
		if d < time.Second {
			d = time.Second
		}
		return d, -1 // 上游精确时刻，无等级概念
	}
	key := modelKey(model)
	// 读取-判定-升级-写回在同一临界区：否则错峰读取会让一批并发失败
	// 把退避等级逐个推高（实测 2→4、4→7 跳档）。
	p.mu.Lock()
	level := acc.backoffLevel[key]
	until := acc.modelCooldown[key]
	if until.After(time.Now()) {
		p.mu.Unlock()
		return p.backoffDuration(level - 1), level
	}
	d := p.backoffDuration(level)
	if level < 16 {
		acc.backoffLevel[key] = level + 1
	}
	p.mu.Unlock()
	return d, level
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

// cooldownModel 冷却**某个账号上的某个模型**（限流走这里）。
//
// 关键不变量：同一 (账号, 模型) 上**只延长、不缩短**。并发请求可能各自
// 判定出不同时长（一个 60s、一个 5s），无条件覆盖会让后写者砍短已判定的
// 限流窗口。不同模型之间互不影响——这正是上游语义。
func (p *Pool) cooldownModel(acc *poolAccount, model string, d time.Duration, msg string) {
	if d <= 0 {
		return
	}
	if d > maxCooldown {
		d = maxCooldown
	}
	now := time.Now()
	until := now.Add(d)
	key := modelKey(model)

	p.mu.Lock()
	defer p.mu.Unlock()
	if prev, ok := acc.modelCooldown[key]; ok && prev.After(now) && prev.After(until) {
		// 已有更晚的存活冷却 → 不缩短（过期的不算，否则会永久锁死）。
		return
	}
	acc.modelCooldown[key] = until
	acc.lastErr = msg
}

// cooldownAccount 冷却**整个账号**（鉴权失效等与模型无关的终态）。
func (p *Pool) cooldownAccount(acc *poolAccount, d time.Duration, reason, msg string) {
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

// AccountLabeler 由带账号归因的流实现。
//
// 用可选接口而不是往 llm.ResponseStream 里加方法：下游协议编码器不关心
// 账号是谁，只有 app 层的观测需要它。
type AccountLabeler interface {
	AccountLabel() string
}

// labeledStream 把账号标签附加到流上，Recv/Close 全部透传。
type labeledStream struct {
	llm.ResponseStream
	label string
}

// AccountLabel 实现 AccountLabeler。
func (s *labeledStream) AccountLabel() string { return s.label }

func (s *labeledStream) Close() error { return closeStream(s.ResponseStream) }

// AccountOf 从流中提取账号标签；无标签（单账号模式）返回空串。
func AccountOf(s llm.ResponseStream) string {
	if l, ok := s.(AccountLabeler); ok {
		return l.AccountLabel()
	}
	return ""
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

// ModelsByAccount 返回每个账号的可用模型 id 集合（模型×账号矩阵用）。
//
// 逐账号查询，失败记为空集合而不是整体失败——某个账号掉线不该让矩阵整块不可用。
func (p *Pool) ModelsByAccount(ctx context.Context) map[string][]string {
	p.mu.Lock()
	accs := append([]*poolAccount(nil), p.accounts...)
	p.mu.Unlock()

	out := make(map[string][]string, len(accs))
	for _, acc := range accs {
		models, err := acc.adp.ListModels(ctx)
		if err != nil {
			out[acc.label] = nil
			continue
		}
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		out[acc.label] = ids
	}
	return out
}

// ListModels 实现 adapter.Adapter：用第一个健康账号的模型目录。
// 同平台账号的目录应当一致；全冷却时退回第一个账号让其报出真实错误。
func (p *Pool) ListModels(ctx context.Context) ([]ModelInfo, error) {
	p.mu.Lock()
	// 列模型不针对特定模型，传空串：只要求账号本身可用。
	acc := p.pickLocked(time.Now(), "")
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
		if acc.state == stateDisabled {
			// 手动停用：不健康但不是故障，且没有冷却倒计时。
			st.State = stateDisabled.String()
			st.Reason = "disabled"
			st.LastError = acc.lastErr
			out = append(out, st)
			continue
		}
		// 模型级冷却：即使账号整体健康，也可能有部分模型被限。
		for m, until := range acc.modelCooldown {
			if s := int(until.Sub(now).Seconds()); s > 0 {
				if st.ModelCooldowns == nil {
					st.ModelCooldowns = map[string]int{}
				}
				st.ModelCooldowns[m] = s
			}
		}
		if acc.accountUsable(now) {
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

// HealthyCount 返回当前可用的账号数（账号级，与模型无关）。
func (p *Pool) HealthyCount() int {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, acc := range p.accounts {
		if acc.accountUsable(now) {
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

// PoolController 是号池的操作面（控制台用）。
//
// 与控制台既有的 Describer/Configurable 同一模式：可选接口，
// 不实现的控制台自然禁用对应按钮。
type PoolController interface {
	// SetAccountEnabled 启用/停用某个账号（按 label 定位，不重启即生效）。
	SetAccountEnabled(label string, enabled bool) error
	// ResetAccountCooldown 手动清除某个账号的冷却（用于「上游已恢复但我还在等」）。
	ResetAccountCooldown(label string) error
}

// SetAccountEnabled 启用或停用账号。停用后不参与调度，直到再次启用。
func (p *Pool) SetAccountEnabled(label string, enabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, acc := range p.accounts {
		if acc.label != label {
			continue
		}
		if enabled {
			if acc.state == stateDisabled {
				acc.state = stateReady
				acc.cooldownUntil = time.Time{}
				acc.modelCooldown = map[string]time.Time{}
				acc.backoffLevel = map[string]int{}
				acc.lastErr = ""
			}
		} else {
			acc.state = stateDisabled
			acc.cooldownUntil = time.Time{}
		}
		p.logfNow("账号 %s 已%s", label, map[bool]string{true: "启用", false: "停用"}[enabled])
		return nil
	}
	return &llm.Failure{Code: "account_not_found", Message: "账号不存在: " + label, ClientFixable: true}
}

// ResetAccountCooldown 清除冷却，让账号立刻回到调度。
func (p *Pool) ResetAccountCooldown(label string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, acc := range p.accounts {
		if acc.label != label {
			continue
		}
		if acc.state == stateDisabled {
			return &llm.Failure{Code: "account_disabled", Message: "账号已停用，请先启用: " + label, ClientFixable: true}
		}
		acc.state = stateReady
		acc.cooldownUntil = time.Time{}
		acc.modelCooldown = map[string]time.Time{}
		acc.backoffLevel = map[string]int{}
		acc.lastErr = ""
		p.logfNow("账号 %s 的冷却已手动清除（含全部模型）", label)
		return nil
	}
	return &llm.Failure{Code: "account_not_found", Message: "账号不存在: " + label, ClientFixable: true}
}

// AccountLabels 返回账号 label 列表（供控制台操作面做参数校验）。
func (p *Pool) AccountLabels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.accounts))
	for _, acc := range p.accounts {
		out = append(out, acc.label)
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
	_ Adapter        = (*Pool)(nil)
	_ Describer      = (*Pool)(nil)
	_ Configurable   = (*Pool)(nil)
	_ PoolController = (*Pool)(nil)
)

// markFailure 记一次账号失败，降低其健康度。调用方不得持有 p.mu。
func (p *Pool) markFailure(acc *poolAccount) {
	p.mu.Lock()
	defer p.mu.Unlock()
	acc.markFailure()
}
