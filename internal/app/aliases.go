package app

import (
	"sort"
	"strings"

	"github.com/576469377/Agent2API/internal/adapter"
)

// 模型别名（model aliases）。
//
// 动机：客户端会发固定的模型名——Claude Code 发 claude-sonnet-4-5-*，Codex 发
// gpt-5-*——而这些名字往往并不在账号的可用目录里。没有别名时用户只能改客户端
// 配置或改模型名，一旦客户端升级就再次失效。
//
// 别名表把「客户端要的名字」与「账号实际有的模型」解耦：请求进网关先查表，
// 命中就换成目标模型（可指定平台）再路由，客户端完全无感。
// 这就是 sub2api 的 composite groups（管理员路由层）的轻量版——区别是
// 它放在配置里，而不是数据库 + 管理界面。
//
// 设计取舍：
//   - **不递归**：别名指向的名字必须是真实模型，避免 a→b→a 这类环。
//   - **容错**：平台名未知（写错、平台没配）时退化为普通模型名并告警，
//     不让一个配置笔误把整个网关变成不可用。
//   - 别名只影响「路由与发往上游的模型名」，会话亲和、号池调度都不变。

// aliasTarget 是别名解析结果。
type aliasTarget struct {
	// Model 是发往上游的模型名。
	Model string
	// Platform 是目标平台 ID；空表示「按 Model 自己路由」（模型索引决定）。
	Platform string
}

// SetAliases 装载别名表。装配期调用（Hub 构造之后、开始服务之前）。
func (h *Hub) SetAliases(aliases map[string]string) {
	if len(aliases) == 0 {
		return
	}
	parsed := make(map[string]aliasTarget, len(aliases))
	for from, to := range aliases {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from == "" || to == "" || from == to {
			continue // 空条目与自我映射没有意义
		}
		t := aliasTarget{Model: to}
		// "平台/模型" 形态：仅当平台真实存在时才当作平台限定。
		if i := strings.Index(to, "/"); i > 0 {
			pid, model := to[:i], to[i+1:]
			if model != "" && h.Platform(pid) != nil {
				t = aliasTarget{Model: model, Platform: pid}
			} else {
				h.logger.Printf("别名 %s → %s 的平台 %q 不存在，按普通模型名处理", from, to, pid)
			}
		}
		parsed[from] = t
	}
	h.mu.Lock()
	h.aliases = parsed
	h.mu.Unlock()
	if len(parsed) > 0 {
		h.logger.Printf("已装载 %d 条模型别名", len(parsed))
	}
}

// ResolveAlias 把请求里的模型名解析成「实际发往上游的模型 + 目标平台 ID」。
//
// 未命中别名时原样返回（platform 为空）。platform 为空表示让 Hub.Route
// 按目标模型名自己找平台。
func (h *Hub) ResolveAlias(model string) (string, string) {
	h.mu.RLock()
	t, ok := h.aliases[model]
	h.mu.RUnlock()
	if !ok {
		return model, ""
	}
	return t.Model, t.Platform
}

// Aliases 返回排序后的别名清单（供模型目录展示）。
func (h *Hub) Aliases() []aliasEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]aliasEntry, 0, len(h.aliases))
	for from, t := range h.aliases {
		out = append(out, aliasEntry{From: from, To: t.Model, Platform: t.Platform})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// aliasEntry 是一条别名的展示形态。
type aliasEntry struct {
	From     string
	To       string
	Platform string
}

// aliasModelInfos 把别名表转成模型目录项，让客户端/控制台能发现它们
// （客户端通常只认列表里出现过的模型名）。
func aliasModelInfos(entries []aliasEntry) []ModelInfo {
	out := make([]ModelInfo, 0, len(entries))
	for _, e := range entries {
		name := e.To
		if e.Platform != "" {
			name = e.Platform + "/" + e.To
		}
		out = append(out, ModelInfo{
			ModelInfo: adapter.ModelInfo{
				ID:          e.From,
				DisplayName: "别名 → " + name,
				Description: "由 models.aliases 映射到 " + name,
			},
			Platform: "(别名)",
		})
	}
	return out
}

// RouteWithHint 在 Route 之上支持别名指定的平台：hint 非空且该平台存在时优先。
func (h *Hub) RouteWithHint(model, platformHint string) *PlatformRuntime {
	if platformHint != "" {
		if rt := h.Platform(platformHint); rt != nil {
			return rt
		}
	}
	return h.Route(model)
}
