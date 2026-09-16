package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

// buildAdapter 按配置装配上游：单账号返回普通适配器；
// 配置了 accounts_dir 且目录里有多个凭证时返回号池（轮询 + 冷却）。
//
// 返回 *adapter.Adapter 接口与一个可选的 *adapter.Pool（供横幅展示账号明细）。
// 任一账号加载失败只记日志不阻断启动——多账号场景下坏一个文件不该拖死网关；
// 全部失败时才返回错误。
func buildAdapter(cfg config.Config, logger interface{ Printf(string, ...any) }) (adapter.Adapter, *adapter.Pool, error) {
	paths := collectCredentialPaths(cfg)

	base := workbuddy.Config{
		BaseURL:            cfg.Upstream.BaseURL,
		Sanitize:           cfg.Upstream.Sanitize,
		RequestTimeout:     cfg.RequestTimeout(),
		StreamIdleTimeout:  cfg.StreamIdleTimeout(),
		StreamTotalTimeout: cfg.StreamTotalTimeout(),
		ModelCacheTTL:      cfg.ModelCacheTTL(),
		Logger: func(format string, args ...any) {
			logger.Printf(format, args...)
		},
	}

	logf := func(format string, args ...any) { logger.Printf(format, args...) }
	pool := adapter.NewPool(cfg.Upstream.Platform, logf)
	loaded := 0
	seenUID := map[string]bool{} // 同一账号不得以两份凭证入池：独立刷新链会互相顶掉会话
	for _, p := range paths {
		one := base
		one.CredentialPath = p
		adp, err := workbuddy.New(one)
		if err != nil {
			// 单账号加载失败不阻断：凭证损坏/过期文件跳过，其余照常入池。
			logf("跳过凭证 %s: %v", filepath.Base(p), err)
			continue
		}
		// 同一账号不得以两份凭证入池：两条独立刷新链会互相顶掉会话
		//（若上游 refresh token 是一次轮换型）。uid 是稳定唯一标识。
		if uid := adp.AccountUID(); uid != "" {
			if seenUID[uid] {
				logf("跳过凭证 %s: 与已加载账号重复（uid=%s），同一账号多份凭证会互相顶掉会话",
					filepath.Base(p), uid)
				continue
			}
			seenUID[uid] = true
		}
		pool.Add(filepath.Base(p), adp)
		loaded++
	}

	switch {
	case loaded == 0:
		return nil, nil, &llm.Failure{
			Code:    "no_credential",
			Message: "未找到任何可用凭证。请先用桌面客户端登录，或运行 agent2api login；多账号请把凭证文件放进 accounts_dir",
		}
	case loaded == 1:
		// 单账号：直接用适配器，省掉一层池调度。
		sts := pool.Statuses()
		return sts[0].Adapter, pool, nil
	default:
		return pool, pool, nil
	}
}

// collectCredentialPaths 汇总候选凭证文件：
// 显式 CredentialPath 优先，其次 accounts_dir 目录下全部 *.json（按文件名排序保证稳定）。
// 单账号的自动探测路径（桌面客户端凭证等）在 CredentialPath 为空时也参与。
func collectCredentialPaths(cfg config.Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	if cfg.Upstream.CredentialPath != "" {
		add(cfg.Upstream.CredentialPath)
	} else {
		// 与原单账号行为一致：让适配器自己探测（桌面客户端凭证等）。
		add("")
	}

	if dir := cfg.Upstream.AccountsDir; dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// 目录不存在不算错误：单账号模式照常工作。
			return out
		}
		var files []string
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			files = append(files, filepath.Join(dir, e.Name()))
		}
		sort.Strings(files)
		for _, f := range files {
			add(f)
		}
	}
	return out
}

// summarizeAccounts 生成横幅/日志用的账号摘要。
func summarizeAccounts(adp adapter.Adapter, pool *adapter.Pool) string {
	if pool == nil || pool.Len() <= 1 {
		if d, ok := adp.(interface{ CredentialInfo() string }); ok {
			return d.CredentialInfo()
		}
		return ""
	}
	healthy := pool.HealthyCount()
	total := pool.Len()
	suffix := ""
	if healthy < total {
		suffix = fmt.Sprintf("（%d/%d 可用，其余冷却中）", healthy, total)
	}
	return fmt.Sprintf("%d 个账号轮询%s", total, suffix)
}
