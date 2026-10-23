package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

type Adapter struct {
	cfg      Config
	mu       sync.Mutex
	models   []adapter.ModelInfo
	expires  time.Time
	ready    bool
	pending  map[string]*job
	benefits adapter.BenefitCache
}

func New(cfg Config) (*Adapter, error) {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.StreamTotalTimeout <= 0 {
		cfg.StreamTotalTimeout = 30 * time.Minute
	}
	if cfg.ModelCacheTTL <= 0 || cfg.ModelCacheTTL > time.Minute {
		cfg.ModelCacheTTL = time.Minute
	}
	if cfg.WorkRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		cfg.WorkRoot = filepath.Join(home, ".agent2api", "kiro-bridge")
	}
	root, err := filepath.Abs(cfg.WorkRoot)
	if err != nil {
		return nil, err
	}
	cfg.WorkRoot = root
	return &Adapter{cfg: cfg, pending: map[string]*job{}}, nil
}
func (a *Adapter) Name() string { return Platform }
func (a *Adapter) Describe() adapter.Description {
	a.mu.Lock()
	ready, count := a.ready, len(a.models)
	a.mu.Unlock()
	d := adapter.Description{ID: Platform, Name: "Kiro Free", BaseURL: "kiro-cli://acp", Account: "官方 CLI Free 账号", Status: "degraded", Notes: "通过官方 Kiro CLI 网页登录；仅接入可自选的个人 Free 模型"}
	if _, err := ResolveCLI(a.cfg.CLIPath); err != nil {
		d.Status = "unauthenticated"
		d.Notes = "请先安装官方 Kiro CLI，再完成账号网页登录"
	} else if ready {
		d.Status = "active"
		d.Models = count
		d.Notes = "官方 ACP / MCP；每次生成核验 Free 月度额度；调用方执行工具"
	}
	return d
}
func (a *Adapter) InvalidateModels() {
	a.mu.Lock()
	a.expires = time.Time{}
	a.ready = false
	a.mu.Unlock()
}
func (a *Adapter) CLIPath() string { return a.cfg.CLIPath }

func (a *Adapter) inspect(ctx context.Context) ([]adapter.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancel()
	exe, err := ResolveCLI(a.cfg.CLIPath)
	if err != nil {
		return nil, err
	}
	work, err := prepareWorkspace(a.cfg.WorkRoot)
	if err != nil {
		return nil, clientFailure("kiro_workspace", "无法准备 Kiro 独立工作目录")
	}
	defer removeWorkspace(a.cfg.WorkRoot, work)
	c, err := startRPC(ctx, exe, work, nil)
	if err != nil {
		return nil, err
	}
	defer c.close()
	id, available, err := initialize(ctx, c, work, []any{})
	if err != nil {
		return nil, err
	}
	u, err := readUsage(ctx, c, id)
	if err != nil {
		return nil, err
	}
	a.recordUsage(u)
	remaining, err := verifyFree(u)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	models := append([]adapter.ModelInfo(nil), a.models...)
	fresh := time.Now().Before(a.expires) && len(models) > 0
	a.mu.Unlock()
	if !fresh {
		raw, err := readCLIModels(ctx, exe, work)
		if err != nil {
			return nil, err
		}
		models, err = decodeModels(raw, remaining, time.Now())
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.models = append([]adapter.ModelInfo(nil), models...)
		a.expires = time.Now().Add(a.cfg.ModelCacheTTL)
		a.mu.Unlock()
	}
	allowed := map[string]bool{}
	for _, v := range available {
		allowed[v] = true
	}
	current := []adapter.ModelInfo{}
	for _, m := range models {
		if !allowed[m.ID] {
			continue
		}
		access := *m.Access
		access.RemainingCredits = remaining
		access.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		m.Access = &access
		current = append(current, m)
	}
	if len(current) == 0 {
		return nil, clientFailure("kiro_no_verified_models", "Kiro 当前会话未提供已验证的免费模型")
	}
	return current, nil
}
func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	models, err := a.inspect(ctx)
	if err != nil && !freeCreditsExhausted(err) {
		_ = a.benefits.Refresh(ctx, Platform, func(context.Context) (adapter.AccountBenefits, error) { return adapter.AccountBenefits{}, err })
	}
	a.mu.Lock()
	a.ready = err == nil
	a.mu.Unlock()
	return models, err
}
func (a *Adapter) VerifyConnection(ctx context.Context) error {
	_, err := a.ListModels(ctx)
	return err
}

func requestBinding(r llm.RequestMessages) string {
	// The gateway's fallback affinity hash includes the wire representation of
	// cache hints. They can move between tool turns without changing meaning.
	// Canonical history below already binds this continuation; keep only explicit
	// session identities here, rather than an unstable inferred routing hint.
	session := r.SessionKey
	if strings.HasPrefix(session, "h:") {
		session = ""
	}
	blocks := withoutCacheHints(r.SystemBlocks)
	tools := append([]llm.ToolDefinition(nil), r.Tools...)
	for i := range tools {
		tools[i].CacheControl = nil
	}
	raw, _ := json.Marshal(struct {
		Model, System string
		Session       string
		Blocks        []llm.Content
		Tools         []llm.ToolDefinition
	}{r.Model, r.SystemPrompt, session, blocks, tools})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (a *Adapter) Stream(ctx context.Context, r llm.RequestMessages) (llm.ResponseStream, error) {
	if !verifiedModels[r.Model] {
		return nil, clientFailure("kiro_verified_model_required", "Kiro 仅允许当前已验证的明确免费模型，不透传 Auto、未知模型或付费模型")
	}
	if r.JSONMode || len(r.Stop) > 0 {
		return nil, clientFailure("kiro_unsupported_request", "Kiro 官方 ACP 桥接暂不支持 JSON 模式或 stop")
	}
	for _, m := range r.Messages {
		for _, b := range m.Content {
			image := b.Type == llm.ContentImage
			if b.ToolResult != nil {
				for _, block := range b.ToolResult.Blocks {
					image = image || block.Type == llm.ContentImage
				}
			}
			if image {
				return nil, clientFailure("kiro_unsupported_request", "Kiro 当前仅验证文本和调用方工具，图片尚未开放")
			}
		}
	}
	// A tool result resumes its exact pending agent, never a second billed prompt.
	var resume *job
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if b.Type != llm.ContentToolResult || b.ToolResult == nil {
				continue
			}
			a.mu.Lock()
			j := a.pending[b.ToolResult.ToolCallID]
			a.mu.Unlock()
			if j != nil {
				if resume != nil && resume != j {
					return nil, clientFailure("kiro_tool_session_mismatch", "工具结果来自不同 Kiro 会话")
				}
				resume = j
			}
		}
	}
	if resume != nil {
		return resume.resume(ctx, r)
	}
	if len(r.Messages) > 0 {
		for _, b := range r.Messages[len(r.Messages)-1].Content {
			if b.ToolResult != nil && strings.HasPrefix(b.ToolResult.ToolCallID, "kiro_call_") {
				return nil, clientFailure("kiro_tool_session_expired", "Kiro 原工具会话已结束或网关已重启，请重新发起请求")
			}
		}
	}
	if r.ToolChoice != nil && (r.ToolChoice.Mode == llm.ToolChoiceRequired || r.ToolChoice.Mode == llm.ToolChoiceFunction) && len(r.Tools) == 0 {
		return nil, clientFailure("kiro_tool_choice", "要求工具调用时必须提供工具定义")
	}
	if r.ToolChoice != nil && r.ToolChoice.Mode == llm.ToolChoiceFunction {
		found := false
		for _, t := range r.Tools {
			found = found || t.Name == r.ToolChoice.FunctionName
		}
		if !found {
			return nil, clientFailure("kiro_tool_choice", "指定工具不在当前工具定义中")
		}
	}
	models, err := a.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, m := range models {
		found = found || m.ID == r.Model
	}
	if !found {
		return nil, clientFailure("kiro_model_unavailable", "此 Kiro Free 账号当前不能选择该模型")
	}
	return a.startJob(ctx, r)
}

func removeWorkspace(root, path string) {
	r, e1 := filepath.Abs(root)
	p, e2 := filepath.Abs(path)
	if e1 != nil || e2 != nil || filepath.Dir(p) != r || len(filepath.Base(p)) < 9 || filepath.Base(p)[:8] != "session-" {
		return
	}
	st, err := os.Lstat(p)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return
	}
	rr, er := filepath.EvalSymlinks(r)
	pp, ep := filepath.EvalSymlinks(p)
	if er != nil || ep != nil || filepath.Dir(pp) != rr {
		return
	}
	_ = os.RemoveAll(p) // Only the verified, directly owned disposable workspace.
}
