package app

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

// ModelInfo 是带「来源平台」的模型目录项。
//
// 多平台集成后，/api/models 与 /v1/models 返回所有平台的模型合并清单；
// Platform 字段是模型的来源平台 ID，控制台据此分类筛选，客户端据此理解归属。
type ModelInfo struct {
	adapter.ModelInfo
	Platform string `json:"platform"`
}

// PlatformRuntime 是一个上游平台在进程内的运行期实例。
//
// Hub 之下、Adapter 之上的一层：Hub 管「有哪些平台、模型怎么路由」，
// PlatformRuntime 管「这一个平台的适配器、号池、登录会话」。
type PlatformRuntime struct {
	// ID 是平台标识（如 workbuddy），与 config.PlatformConfig.ID 一致。
	ID string
	// Adapter 是该平台的适配器（单账号或号池）。
	Adapter adapter.Adapter
	// Pool 是该平台的号池；单账号模式为 nil。
	Pool *adapter.Pool
	// Cfg 是该平台自己的配置（凭证目录 / 上游地址 / 超时）。
	Cfg config.PlatformConfig
}

// platformDefaultBaseURLs 是各平台的默认上游地址，
// 平台配置未显式给 base_url 时兜底（与各适配器内置默认一致）。
var platformDefaultBaseURLs = map[string]string{
	"workbuddy": "https://copilot.tencent.com",
}

// Hub 是多平台集成的核心：一个进程里管着所有上游平台，
// 按模型名把每个请求路由到拥有它的平台。
//
// 路由语义（设计决策，见 config.PlatformConfig 注释）：
//   - 请求的 model 能在任一平台的目录里找到 → 路由到那个平台；
//   - 找不到 → 路由到「默认平台」（配置列表里的第一个）。
//
// 第二条是刻意的向后兼容：单平台时代网关把任意 model 原样透传给上游，
// 上游自己决定怎么处理未知模型名；多平台下保持同样的透传语义，
// 只是把「透传目标」变成了默认平台。
type Hub struct {
	mu sync.RWMutex
	// platforms 平台 ID → 运行期实例；order 保持配置声明顺序。
	platforms map[string]*PlatformRuntime
	order     []string
	// index 模型 ID → 平台 ID。启动时从各平台 ListModels 构建；
	// 模型 ID 冲突时首个平台胜出并告警——两平台同名模型必然有一个路由不到。
	index map[string]string

	logger *log.Logger
}

// NewHub 由各平台已建好的运行期实例装配出多平台枢纽。
//
// 装配期做三件事：登记平台、给每个平台建独立的登录会话管理、
// 构建模型索引（model ID → 平台）。模型枚举失败不阻断启动——
// 那个平台只是暂时没有可路由模型，与单平台时代「启动不强依赖上游」一致。
func NewHub(runtimes []*PlatformRuntime, logger *log.Logger) (*Hub, error) {
	if logger == nil {
		logger = log.New(nil, "", 0)
	}
	h := &Hub{
		platforms: map[string]*PlatformRuntime{},
		index:     map[string]string{},
		logger:    logger,
	}
	for _, rt := range runtimes {
		if rt == nil || rt.ID == "" || rt.Adapter == nil {
			return nil, fmt.Errorf("平台运行期实例不完整（缺 ID 或 Adapter）")
		}
		if _, dup := h.platforms[rt.ID]; dup {
			return nil, fmt.Errorf("平台重复配置: %s", rt.ID)
		}
		h.platforms[rt.ID] = rt
		h.order = append(h.order, rt.ID)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		models, err := rt.Adapter.ListModels(ctx)
		cancel()
		if err != nil {
			h.logger.Printf("平台 %s 模型枚举失败（不影响启动，稍后可刷新）: %v", rt.ID, err)
			continue
		}
		for _, m := range models {
			if owner, exists := h.index[m.ID]; exists {
				h.logger.Printf("模型 %s 同时存在于 %s 与 %s，路由到先配置的 %s", m.ID, owner, rt.ID, owner)
				continue
			}
			h.index[m.ID] = rt.ID
		}
	}
	if len(h.platforms) == 0 {
		return nil, &llm.Failure{
			Code:    "no_platform",
			Message: "没有任何可用平台。请检查配置里的 upstream.platforms（或单平台字段）",
		}
	}
	return h, nil
}

// Route 按模型名找到应该处理这次请求的平台。
//
// 精确命中模型索引 → 那个平台；未命中 → 默认平台（透传语义，见 Hub 注释）。
// 返回的 *PlatformRuntime 恒非 nil（除非 Hub 为空，而构造期已排除这种情况）。
func (h *Hub) Route(model string) *PlatformRuntime {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if pid, ok := h.index[model]; ok {
		return h.platforms[pid]
	}
	return h.DefaultPlatform()
}

// Platforms 按配置声明顺序返回所有平台运行期实例。
func (h *Hub) Platforms() []*PlatformRuntime {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*PlatformRuntime, 0, len(h.order))
	for _, id := range h.order {
		if rt, ok := h.platforms[id]; ok {
			out = append(out, rt)
		}
	}
	return out
}

// Platform 取指定 ID 的平台；不存在返回 nil。
func (h *Hub) Platform(id string) *PlatformRuntime {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.platforms[id]
}

// DefaultPlatform 返回默认平台（配置列表里的第一个）。
// 未知模型名会透传给它，保持单平台时代的语义。
func (h *Hub) DefaultPlatform() *PlatformRuntime {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.order) == 0 {
		return nil
	}
	return h.platforms[h.order[0]]
}

// OwnerOf 返回模型 ID 的所属平台 ID；未知返回空串。
func (h *Hub) OwnerOf(model string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.index[model]
}

// Describe 汇总所有平台的运行时描述（供 /api/platforms 与横幅）。
//
// 单个平台探测失败不影响其它平台——每个平台独立 Describe，错误只记日志。
func (h *Hub) Describe() []adapter.Description {
	out := make([]adapter.Description, 0, len(h.order))
	for _, rt := range h.Platforms() {
		if d, ok := rt.Adapter.(adapter.Describer); ok {
			out = append(out, d.Describe())
			continue
		}
		out = append(out, adapter.Description{
			ID:     rt.ID,
			Name:   rt.ID,
			Status: "active",
			Notes:  "该平台未实现 Describer 接口",
		})
	}
	return out
}

// ListModels 聚合所有平台的模型清单，每项带来源平台。
//
// 某个平台枚举失败时跳过它（记日志），不让一个平台的上游抖动拖垮整个清单。
func (h *Hub) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var out []ModelInfo
	for _, rt := range h.Platforms() {
		ms, err := rt.Adapter.ListModels(ctx)
		if err != nil {
			h.logger.Printf("平台 %s 模型枚举失败: %v", rt.ID, err)
			continue
		}
		for _, m := range ms {
			out = append(out, ModelInfo{ModelInfo: m, Platform: rt.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// InvalidateModels 让所有平台的模型缓存失效（控制台「刷新」按钮）。
func (h *Hub) InvalidateModels() {
	for _, rt := range h.Platforms() {
		if c, ok := rt.Adapter.(adapter.Configurable); ok {
			c.InvalidateModels()
		}
	}
}

// baseURL 返回该平台的上游地址：平台配置优先，缺省回退到平台默认值。
func (rt *PlatformRuntime) baseURL() string {
	if rt.Cfg.BaseURL != "" {
		return rt.Cfg.BaseURL
	}
	if u, ok := platformDefaultBaseURLs[rt.ID]; ok {
		return u
	}
	if rt.Adapter != nil {
		return "https://" + rt.Adapter.Name()
	}
	return ""
}

// AccountsDir 返回该平台的号池目录（可能为空 = 未配置号池）。
func (rt *PlatformRuntime) AccountsDir() string { return rt.Cfg.AccountsDir }

// accountStatusLister 由号池实现；与 api.go 中的接口定义同包共用。
// （单账号适配器不实现它 —— 见 AccountStatuses 的合成逻辑。）

// AccountStatuses 返回该平台的账号状态列表（统一号池视图的数据源）。
//
// 号池适配器直接透传 Statuses()；单账号适配器（桌面凭证直连）没有号池，
// 从 Describe() 合成一条记录 —— 与 sub2api 一致：哪怕只有一个上游账号，
// 它也是号池里可见、可管理的一员，provider 只是账号属性。
// 注意：合成路径会触发一次 Describe 探测（轻量登录态检查）。
func (rt *PlatformRuntime) AccountStatuses() []adapter.AccountStatus {
	if lister, ok := rt.Adapter.(accountStatusLister); ok {
		return lister.Statuses()
	}
	d := adapter.Description{Status: "active", Name: rt.ID}
	if dd, ok := rt.Adapter.(adapter.Describer); ok {
		d = dd.Describe()
	}
	label := d.Account
	if label == "" {
		label = "(默认账号)"
	}
	st := adapter.AccountStatus{
		Label:   label,
		Healthy: d.Status == "active",
		Adapter: rt.Adapter,
	}
	if st.Healthy {
		st.State = "ready"
	} else {
		st.State = "error"
		st.Reason = "unhealthy"
		st.LastError = d.Notes
	}
	return []adapter.AccountStatus{st}
}
