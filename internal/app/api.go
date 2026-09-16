package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/llm"
)

// Version 是网关版本。
const Version = "0.2.0"

// 计划接入但尚未实现的平台。
//
// 控制台会显式展示这些条目：一来让「多平台」这件事在界面上可见，
// 二来新增平台时只需在适配器注册表里加一项，前端无需改动。
var plannedPlatforms = []plannedPlatform{
	{ID: "devin", Name: "Devin", Note: "上游为私有 protobuf over Connect，需独立适配器"},
	{ID: "cursor", Name: "Cursor", Note: "待调研上游协议"},
}

type plannedPlatform struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note"`
}

// ───────────────────────── 状态 ─────────────────────────

type statusResponse struct {
	Version     string `json:"version"`
	UptimeSec   int64  `json:"uptime_sec"`
	GoVersion   string `json:"go_version"`
	Platform    string `json:"platform"`
	PlatformID  string `json:"platform_id"`
	Account     string `json:"account,omitempty"`
	Listen      string `json:"listen"`
	AuthEnabled bool   `json:"auth_enabled"`
	Time        string `json:"time"`
}

func (a *App) apiStatus(w http.ResponseWriter, r *http.Request) {
	desc := a.describePlatform()
	writeJSON(w, statusResponse{
		Version:     Version,
		UptimeSec:   int64(time.Since(a.startedAt).Seconds()),
		GoVersion:   runtime.Version(),
		Platform:    a.adapter.Name(),
		PlatformID:  desc.ID,
		Account:     desc.Account,
		Listen:      a.cfg.Addr(),
		AuthEnabled: a.cfg.Auth.APIKey != "",
		Time:        time.Now().Format(time.RFC3339),
	})
}

// apiAuthHint 是唯一一个**不要求鉴权**的管理端点。
//
// 它只暴露「网关是否启用了鉴权」这一个布尔值——这本身不是敏感信息
// （探测 /v1/chat/completions 是否 401 也能得到同样结论），却能让网页控制台
// 在 401 时给出可行动的提示：弹出密钥输入框，而不是一脸懵地白屏。
// 除这个布尔外什么都不返回，避免成为未鉴权的信息出口。
func (a *App) apiAuthHint(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"auth_enabled": a.cfg.Auth.APIKey != ""})
}

// ───────────────────────── 指标 ─────────────────────────

func (a *App) apiMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.metrics.Snapshot())
}

// ───────────────────────── 平台 ─────────────────────────

type platformsResponse struct {
	Active  []adapter.Description `json:"active"`
	Planned []plannedPlatform     `json:"planned"`
}

func (a *App) apiPlatforms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, platformsResponse{
		Active:  []adapter.Description{a.describePlatform()},
		Planned: plannedPlatforms,
	})
}

// accountStatusLister 是号池可选实现的接口。
//
// 与控制台既有的 adapter.Describer 同一模式：用可选接口而不是往
// adapter.Adapter 里加方法，避免为了一个展示需求波及所有平台的适配器实现。
type accountStatusLister interface {
	Statuses() []adapter.AccountStatus
}

// apiAccounts 暴露号池里每个账号的运行状态（供控制台号池可视化）。
//
// 单账号模式下返回空数组而非 404，让前端只有一条代码路径。
func (a *App) apiAccounts(w http.ResponseWriter, r *http.Request) {
	type resp struct {
		Accounts []adapter.AccountStatus `json:"accounts"`
		Total    int                     `json:"total"`
		Healthy  int                     `json:"healthy"`
	}
	out := resp{Accounts: []adapter.AccountStatus{}}
	if lister, ok := a.adapter.(accountStatusLister); ok {
		sts := lister.Statuses()
		out.Accounts = sts
		out.Total = len(sts)
		for _, st := range sts {
			if st.Healthy {
				out.Healthy++
			}
		}
	}
	writeJSON(w, out)
}

// ───────────────────────── 账号管理（登录状态 / 重新登录 / 增删） ─────────────────────────

// accountIdentityProvider 由能自报身份与凭证健康度的适配器实现。
type accountIdentityProvider interface {
	AccountIdentity() workbuddy.AccountIdentity
}

// accountLister 由能列出账号的适配器实现（号池或单账号）。
type accountLister interface {
	AccountLabels() []string
}

// apiAccountsManage 是账号管理端点：
//
//	GET    /api/accounts/manage          列出账号 + 登录状态
//	POST   /api/accounts/manage          启动添加账号（设备码登录）
//	POST   /api/accounts/manage?action=enable|disable|reset&label=X
func (a *App) apiAccountsManage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.listManagedAccounts(w)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 GET / POST", http.StatusMethodNotAllowed)
		return
	}

	// 不带 action = 启动一次新登录（添加账号）。
	action := r.URL.Query().Get("action")
	if action == "" {
		a.startAccountLogin(w, r)
		return
	}

	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Label == "" {
		writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "缺少 label"},
		})
		return
	}

	// 账号操作走可选接口：非号池模式（单账号）自然不支持。
	ctl, ok := a.adapter.(adapter.PoolController)
	if !ok {
		writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "当前为单账号模式，不支持账号操作"},
		})
		return
	}

	var err error
	switch action {
	case "enable":
		err = ctl.SetAccountEnabled(body.Label, true)
	case "disable":
		err = ctl.SetAccountEnabled(body.Label, false)
	case "reset":
		err = ctl.ResetAccountCooldown(body.Label)
	default:
		writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "未知操作: " + action},
		})
		return
	}
	if err != nil {
		f := llm.Wrap(err)
		writeJSONWithStatus(w, f.HTTPStatus(), common.BuildErrorPayload(f))
		return
	}
	a.listManagedAccounts(w)
}

// managedAccount 是账号管理视图：调度状态 + 登录状态合并。
type managedAccount struct {
	adapter.AccountStatus
	Identity *workbuddy.AccountIdentity `json:"identity,omitempty"`
}

func (a *App) listManagedAccounts(w http.ResponseWriter) {
	out := struct {
		Accounts    []managedAccount `json:"accounts"`
		Total       int              `json:"total"`
		Healthy     int              `json:"healthy"`
		AccountsDir string           `json:"accounts_dir,omitempty"`
	}{Accounts: []managedAccount{}}

	lister, ok := a.adapter.(accountStatusLister)
	if !ok {
		writeJSON(w, out)
		return
	}
	sts := lister.Statuses()
	out.Total = len(sts)
	for _, st := range sts {
		if st.Healthy {
			out.Healthy++
		}
		m := managedAccount{AccountStatus: st}
		// 登录状态：凭证是否还有效、refreshToken 是否已死。
		if p, ok := st.Adapter.(accountIdentityProvider); ok {
			id := p.AccountIdentity()
			m.Identity = &id
		}
		out.Accounts = append(out.Accounts, m)
	}
	out.AccountsDir = a.cfg.Upstream.AccountsDir
	writeJSON(w, out)
}

// startAccountLogin 发起设备码登录（添加新账号）。
func (a *App) startAccountLogin(w http.ResponseWriter, r *http.Request) {
	dir := a.cfg.Upstream.AccountsDir
	if dir == "" {
		// 未显式配置号池目录时，兜底到 ~/.workbuddy/（workbuddy 自有凭证的
		// 默认目录）。直接报错会把一个纯配置问题抛回给用户——「添加账号」
		// 这个动作本身完全可以在默认位置工作，登录完成后提示用户把该目录
		// 配成 accounts_dir（或下次启动前建 auths/）即可入池。
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{
					"message": "无法确定用户主目录，请用 -accounts-dir 显式指定号池目录",
				},
			})
			return
		}
		dir = filepath.Join(home, ".workbuddy")
		_ = os.MkdirAll(dir, 0o700)
		a.logins.logf("accounts_dir 未配置，本次登录凭证将写入 %s；把它配成 accounts_dir 后即可入池", dir)
	}
	// 新凭证文件名由前端给（默认 account-N）；只接受纯文件名，防目录穿越。
	name := r.URL.Query().Get("name")
	if name == "" {
		name = fmt.Sprintf("account-%d.json", time.Now().Unix())
	}
	if !safeCredentialName(name) {
		writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "非法的凭证文件名（只允许字母数字、点、下划线、短横线）"},
		})
		return
	}
	sess, err := a.logins.Start(filepath.Join(dir, name))
	if err != nil {
		f := llm.Wrap(err)
		writeJSONWithStatus(w, f.HTTPStatus(), common.BuildErrorPayload(f))
		return
	}
	// 把实际落盘位置带回去：前端要在成功提示里告诉用户「凭证在哪、
	// 怎么让它进号池」。
	out := struct {
		*loginSession
		AccountsDir string `json:"accounts_dir_used"`
	}{sess, dir}
	writeJSON(w, out)
}

// apiLoginStatus 轮询一次登录会话。
func (a *App) apiLoginStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "缺少 id"},
		})
		return
	}
	sess, err := a.logins.Get(id)
	if err != nil {
		f := llm.Wrap(err)
		writeJSONWithStatus(w, f.HTTPStatus(), common.BuildErrorPayload(f))
		return
	}
	writeJSON(w, sess)
}

// accountModelLister 由能按账号列模型（号池）的适配器实现。
type accountModelLister interface {
	ModelsByAccount(ctx context.Context) map[string][]string
}

// apiAccountModels 返回模型×账号矩阵。
//
// 单账号模式退化：只有一个账号时返回该账号的模型，前端矩阵自然退化成列表。
func (a *App) apiAccountModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	type resp struct {
		Accounts []string            `json:"accounts"`
		Models   []string            `json:"models"`
		Matrix   map[string][]string `json:"matrix"`
	}
	out := resp{Accounts: []string{}, Models: []string{}, Matrix: map[string][]string{}}

	if lister, ok := a.adapter.(accountModelLister); ok {
		m := lister.ModelsByAccount(ctx)
		seen := map[string]bool{}
		for label, ids := range m {
			out.Accounts = append(out.Accounts, label)
			out.Matrix[label] = ids
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					out.Models = append(out.Models, id)
				}
			}
		}
	} else {
		// 单账号：直接列模型。
		models, err := a.adapter.ListModels(ctx)
		if err != nil {
			f := llm.Wrap(err)
			writeJSONWithStatus(w, f.HTTPStatus(), common.BuildErrorPayload(f))
			return
		}
		out.Accounts = []string{"(单账号)"}
		for _, md := range models {
			out.Models = append(out.Models, md.ID)
			out.Matrix["(单账号)"] = append(out.Matrix["(单账号)"], md.ID)
		}
	}
	sort.Strings(out.Accounts)
	sort.Strings(out.Models)
	writeJSON(w, out)
}

// safeCredentialName 只允许纯文件名，挡住 ../ 之类的路径穿越。
func safeCredentialName(name string) bool {
	if name == "" || len(name) > 128 || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	if name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// writeJSONWithStatus 写指定状态码的 JSON。
func writeJSONWithStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// describePlatform 读取当前平台的运行时描述；适配器未实现 Describer 时给出兜底信息。
func (a *App) describePlatform() adapter.Description {
	if d, ok := a.adapter.(adapter.Describer); ok {
		return d.Describe()
	}
	return adapter.Description{
		ID:     a.adapter.Name(),
		Name:   a.adapter.Name(),
		Status: "active",
		Notes:  "该平台未实现 Describer 接口",
	}
}

// ───────────────────────── 模型 ─────────────────────────

func (a *App) apiModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	models, err := a.adapter.ListModels(ctx)
	if err != nil {
		writeJSON(w, map[string]any{"models": []any{}, "error": llm.Wrap(err).Error()})
		return
	}
	writeJSON(w, map[string]any{
		"platform": a.adapter.Name(),
		"models":   models,
	})
}

// ───────────────────────── 配置 ─────────────────────────

type configResponse struct {
	Listen   string `json:"listen"`
	Auth     bool   `json:"auth_enabled"`
	Sanitize bool   `json:"sanitize"`
	Upstream struct {
		Platform           string `json:"platform"`
		BaseURL            string `json:"base_url"`
		StreamIdleTimeout  string `json:"stream_idle_timeout"`
		StreamTotalTimeout string `json:"stream_total_timeout"`
		RequestTimeout     string `json:"request_timeout"`
		ModelCacheTTL      string `json:"model_cache_ttl"`
	} `json:"upstream"`
}

func (a *App) apiConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, a.buildConfigResponse())
	case http.MethodPost:
		a.updateConfig(w, r)
	default:
		http.Error(w, "仅支持 GET / POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) buildConfigResponse() configResponse {
	var out configResponse
	out.Listen = a.cfg.Addr()
	out.Auth = a.cfg.Auth.APIKey != ""
	out.Sanitize = a.currentSanitize()
	out.Upstream.Platform = a.cfg.Upstream.Platform
	out.Upstream.BaseURL = a.cfg.Upstream.BaseURL
	out.Upstream.StreamIdleTimeout = humanDuration(a.cfg.StreamIdleTimeout())
	out.Upstream.StreamTotalTimeout = humanDuration(a.cfg.StreamTotalTimeout())
	out.Upstream.RequestTimeout = humanDuration(a.cfg.RequestTimeout())
	out.Upstream.ModelCacheTTL = humanDuration(a.cfg.ModelCacheTTL())
	return out
}

// humanDuration 把 time.Duration 渲染为符合中文阅读习惯的紧凑文本，
// 避免出现 Go 原生的 "2m0s" 这类对终端用户不友好的格式。
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	var parts []string
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%d 小时", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%d 分钟", m))
	}
	if s > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d 秒", s))
	}
	return strings.Join(parts, " ")
}

// currentSanitize 读取适配器当前的脱敏开关（若支持运行期查询）。
func (a *App) currentSanitize() bool {
	if c, ok := a.adapter.(adapter.Configurable); ok {
		return c.Sanitize()
	}
	return a.cfg.Upstream.Sanitize
}

type configPatch struct {
	Sanitize      *bool `json:"sanitize"`
	RefreshModels bool  `json:"refresh_models"`
}

// updateConfig 只开放「可以安全热更新」的项。
//
// 端口、上游地址这类改动需要重启，控制台不做假动作——避免用户以为改了却没生效。
func (a *App) updateConfig(w http.ResponseWriter, r *http.Request) {
	var patch configPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&patch); err != nil {
		http.Error(w, "请求体解析失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	applied := map[string]any{}

	if patch.Sanitize != nil {
		c, ok := a.adapter.(adapter.Configurable)
		if !ok {
			http.Error(w, "当前平台不支持运行期修改脱敏开关", http.StatusBadRequest)
			return
		}
		c.SetSanitize(*patch.Sanitize)
		a.cfg.Upstream.Sanitize = *patch.Sanitize
		applied["sanitize"] = *patch.Sanitize
	}
	if patch.RefreshModels {
		if c, ok := a.adapter.(adapter.Configurable); ok {
			c.InvalidateModels()
			applied["refresh_models"] = true
		}
	}
	writeJSON(w, map[string]any{"applied": applied, "config": a.buildConfigResponse()})
}

// ───────────────────────── 工具 ─────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "序列化失败", http.StatusInternalServerError)
	}
}
