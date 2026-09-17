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

// 所有内置平台清单（单一事实来源）。
//
// 控制台据此把「已接入 / 可用（已实现但当前未运行）/ 规划中（未实现）」三类分开展示，
// 新增平台只需在这里加一项，前端无需改动。Implemented=false 的才是真正的「规划中」。
var builtinPlatforms = []struct {
	ID          string
	Name        string
	Note        string
	Implemented bool
}{
	{ID: "workbuddy", Name: "WorkBuddy / CodeBuddy", Note: "腾讯 CodeBuddy 桌面端登录态复用", Implemented: true},
	{ID: "devin", Name: "Devin", Note: "上游为私有 protobuf over Connect，需独立适配器", Implemented: false},
	{ID: "cursor", Name: "Cursor", Note: "待调研上游协议", Implemented: false},
}

// platformEntry 是「规划中 / 可用（已实现但当前未运行）」两类平台共用的精简描述。
type platformEntry struct {
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
	// Platforms 是本进程集成的全部平台（多平台集成的核心字段）。
	// Platform/PlatformID/Account 保留为默认平台的摘要，兼容旧客户端。
	Platforms []adapter.Description `json:"platforms"`
}

func (a *App) apiStatus(w http.ResponseWriter, r *http.Request) {
	descs := a.hub.Describe()
	def := a.hub.DefaultPlatform()
	var defID, defName, defAccount string
	if def != nil {
		defID = def.ID
		defName = def.Adapter.Name()
	}
	for _, d := range descs {
		if d.ID == defID {
			defAccount = d.Account
			if d.Name != "" {
				defName = d.Name
			}
		}
	}
	writeJSON(w, statusResponse{
		Version:     Version,
		UptimeSec:   int64(time.Since(a.startedAt).Seconds()),
		GoVersion:   runtime.Version(),
		Platform:    defName,
		PlatformID:  defID,
		Account:     defAccount,
		Listen:      a.cfg.Addr(),
		AuthEnabled: a.cfg.Auth.APIKey != "",
		Time:        time.Now().Format(time.RFC3339),
		Platforms:   descs,
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
	// Active 是本进程实际集成并在运行的全部平台（多平台集成，不止一个）。
	Active []adapter.Description `json:"active"`
	// Planned 是尚未实现的平台（规划中）。
	Planned []platformEntry `json:"planned"`
}

// apiPlatforms 返回平台总览：已集成运行的平台 + 未实现的规划项。
//
// 「可用（已实现但未运行）」这一类随多平台集成而消失——现在一个控制台
// 集成所有已配置平台，配置了就在跑；想多一个平台，往配置里加一项并重启。
func (a *App) apiPlatforms(w http.ResponseWriter, r *http.Request) {
	active := a.hub.Describe()
	running := map[string]bool{}
	for _, d := range active {
		running[d.ID] = true
	}

	var planned []platformEntry
	for _, p := range builtinPlatforms {
		if !p.Implemented && !running[p.ID] {
			planned = append(planned, platformEntry{ID: p.ID, Name: p.Name, Note: p.Note})
		}
	}

	writeJSON(w, platformsResponse{
		Active:  active,
		Planned: planned,
	})
}

// accountStatusLister 是号池可选实现的接口。
//
// 与控制台既有的 adapter.Describer 同一模式：用可选接口而不是往
// adapter.Adapter 里加方法，避免为了一个展示需求波及所有平台的适配器实现。
type accountStatusLister interface {
	Statuses() []adapter.AccountStatus
}

// platformOr400 解析请求里的 ?platform= 参数并返回对应平台运行期实例。
//
// 未指定时用默认平台（单平台配置下即唯一平台，行为与旧版一致）；
// 指定了但不存在时返回 400——管理操作打错平台名不该静默落到别的平台。
func (a *App) platformOr400(w http.ResponseWriter, r *http.Request) (*PlatformRuntime, bool) {
	id := r.URL.Query().Get("platform")
	if id == "" {
		rt := a.hub.DefaultPlatform()
		if rt == nil {
			writeJSONWithStatus(w, http.StatusServiceUnavailable, map[string]any{
				"error": map[string]string{"message": "没有任何已集成平台"},
			})
			return nil, false
		}
		return rt, true
	}
	rt := a.hub.Platform(id)
	if rt == nil {
		writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "未知平台: " + id},
		})
		return nil, false
	}
	return rt, true
}

// accountStatusView 是带来源平台的账号状态视图（多平台合并号池用）。
type accountStatusView struct {
	adapter.AccountStatus
	Platform string `json:"platform"`
}

// apiAccounts 暴露号池里每个账号的运行状态（供控制台号池可视化）。
//
// 多平台集成后这里返回**全部平台账号的合并号池**，每个账号带 platform 来源——
// 与 sub2api 一致：上游账号是同一个池子里的资源，provider 只是账号属性。
// 单账号模式返回空数组而非 404，让前端只有一条代码路径。
func (a *App) apiAccounts(w http.ResponseWriter, r *http.Request) {
	type resp struct {
		Accounts []accountStatusView `json:"accounts"`
		Total    int                 `json:"total"`
		Healthy  int                 `json:"healthy"`
	}
	out := resp{Accounts: []accountStatusView{}}
	for _, rt := range a.hub.Platforms() {
		for _, st := range rt.AccountStatuses() {
			if st.Healthy {
				out.Healthy++
			}
			out.Accounts = append(out.Accounts, accountStatusView{AccountStatus: st, Platform: rt.ID})
		}
	}
	out.Total = len(out.Accounts)
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

// apiAccountsManage 是账号管理端点（全部平台账号合并为一个号池视图）：
//
//	GET    /api/accounts/manage          列出所有平台的账号 + 登录状态（每项带 platform 来源）
//	POST   /api/accounts/manage?platform=X   添加账号（platform 由前端对话框选择）
//	POST   /api/accounts/manage?action=enable|disable|reset&label=X
//	                                     操作目标平台按 label 自动解析，也可显式 ?platform=
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

	rt, ok := a.resolveAccountPlatform(w, r, body.Label)
	if !ok {
		return
	}

	// 账号操作走可选接口：非号池模式（单账号）自然不支持。
	ctl, ok := rt.Adapter.(adapter.PoolController)
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

// managedAccount 是账号管理视图：调度状态 + 登录状态合并 + 来源平台。
type managedAccount struct {
	adapter.AccountStatus
	Platform string                     `json:"platform"`
	Identity *workbuddy.AccountIdentity `json:"identity,omitempty"`
}

// listManagedAccounts 返回**全部平台账号的合并视图**（控制台「账号」页数据源）。
//
// 与 sub2api 同一模型：上游账号统一在一个号池视图里管理，
// provider（platform 字段）只是账号的属性，前端不需要按平台切换。
func (a *App) listManagedAccounts(w http.ResponseWriter) {
	type platDir struct {
		ID          string `json:"id"`
		AccountsDir string `json:"accounts_dir,omitempty"`
	}
	out := struct {
		Accounts    []managedAccount `json:"accounts"`
		Total       int              `json:"total"`
		Healthy     int              `json:"healthy"`
		AccountsDir string           `json:"accounts_dir,omitempty"`
		Platforms   []platDir        `json:"platforms"`
	}{Accounts: []managedAccount{}}

	for _, rt := range a.hub.Platforms() {
		out.Platforms = append(out.Platforms, platDir{ID: rt.ID, AccountsDir: rt.AccountsDir()})
		if out.AccountsDir == "" {
			out.AccountsDir = rt.AccountsDir()
		}
		for _, st := range rt.AccountStatuses() {
			if st.Healthy {
				out.Healthy++
			}
			m := managedAccount{AccountStatus: st, Platform: rt.ID}
			// 登录状态：凭证是否还有效、refreshToken 是否已死。
			if p, ok := st.Adapter.(accountIdentityProvider); ok {
				id := p.AccountIdentity()
				m.Identity = &id
			}
			out.Accounts = append(out.Accounts, m)
		}
	}
	out.Total = len(out.Accounts)
	writeJSON(w, out)
}

// resolveAccountPlatform 找到 label 所属的平台运行期实例。
//
// 显式 ?platform= 优先；否则扫描各平台的账号列表精确匹配（跨平台同名 label 报错）；
// 都找不到时退回默认平台，让 PoolController 给出准确的错误信息。
func (a *App) resolveAccountPlatform(w http.ResponseWriter, r *http.Request, label string) (*PlatformRuntime, bool) {
	if id := r.URL.Query().Get("platform"); id != "" {
		rt := a.hub.Platform(id)
		if rt == nil {
			writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{"message": "未知平台: " + id},
			})
			return nil, false
		}
		return rt, true
	}
	var hits []*PlatformRuntime
	for _, rt := range a.hub.Platforms() {
		for _, st := range rt.AccountStatuses() {
			if st.Label == label {
				hits = append(hits, rt)
				break
			}
		}
	}
	switch {
	case len(hits) == 1:
		return hits[0], true
	case len(hits) > 1:
		writeJSONWithStatus(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "账号 " + label + " 在多个平台重复，请带 ?platform= 指定"},
		})
		return nil, false
	default:
		rt := a.hub.DefaultPlatform()
		if rt == nil {
			writeJSONWithStatus(w, http.StatusServiceUnavailable, map[string]any{
				"error": map[string]string{"message": "没有任何已集成平台"},
			})
			return nil, false
		}
		return rt, true
	}
}

// startAccountLogin 发起一次「添加账号」登录（设备码授权，按平台分派登录方式，
// 登录方式在 loginManager.Start 内按平台分支）。
// platform 由前端「添加账号」对话框显式选择（sub2api 同款交互）。
func (a *App) startAccountLogin(w http.ResponseWriter, r *http.Request) {
	rt, ok := a.platformOr400(w, r)
	if !ok {
		return
	}
	dir := rt.AccountsDir()
	if dir == "" {
		// 未显式配置号池目录时，兜底到 ~/.workbuddy/（登录凭证的默认位置）。
		// 号池监视器每 10 秒扫描该目录，新凭证无需重启即可加入轮询。
		dir = DefaultAccountsDir()
		_ = os.MkdirAll(dir, 0o700)
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
	sess, err := a.logins.Start(rt.ID, filepath.Join(dir, name))
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

// apiAccountModels 返回模型×账号矩阵（全部平台合并：账号是列，模型是行）。
//
// 跨平台 label 冲突时用「平台/label」区分；单账号平台退化为单列。
func (a *App) apiAccountModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	type resp struct {
		Accounts []string            `json:"accounts"`
		Models   []string            `json:"models"`
		Matrix   map[string][]string `json:"matrix"`
		// Cooldowns 是「账号 → 模型 → 剩余冷却秒数」。
		//
		// 限流是「账号 × 模型」维度的（上游明确说可切换其他模型），所以矩阵里
		// 一个格子既可能「不支持」也可能「支持但暂时被限流」——后者必须能看到
		// 还要等多久，否则用户会以为这个组合坏了。
		Cooldowns map[string]map[string]int `json:"cooldowns,omitempty"`
	}
	out := resp{
		Accounts:  []string{},
		Models:    []string{},
		Matrix:    map[string][]string{},
		Cooldowns: map[string]map[string]int{},
	}
	seen := map[string]bool{}

	merge := func(key string, ids []string, cooling map[string]int) {
		out.Accounts = append(out.Accounts, key)
		out.Matrix[key] = ids
		if len(cooling) > 0 {
			out.Cooldowns[key] = cooling
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out.Models = append(out.Models, id)
			}
		}
	}

	// accountCooldowns 从号池状态里取出「该账号上仍在冷却的模型」。
	// 非号池适配器（单账号）没有这个概念，返回 nil。
	accountCooldowns := func(adp adapter.Adapter) map[string]map[string]int {
		lister, ok := adp.(accountStatusLister)
		if !ok {
			return nil
		}
		out := map[string]map[string]int{}
		for _, st := range lister.Statuses() {
			if len(st.ModelCooldowns) > 0 {
				out[st.Label] = st.ModelCooldowns
			}
		}
		return out
	}

	for _, rt := range a.hub.Platforms() {
		if lister, ok := rt.Adapter.(accountModelLister); ok {
			m := lister.ModelsByAccount(ctx)
			labels := make([]string, 0, len(m))
			for label := range m {
				labels = append(labels, label)
			}
			sort.Strings(labels)
			cooling := accountCooldowns(rt.Adapter)
			for _, label := range labels {
				key := label
				if _, exists := out.Matrix[key]; exists {
					key = rt.ID + "/" + label
				}
				merge(key, m[label], cooling[label])
			}
			continue
		}
		// 单账号平台：直接列模型。
		ms, err := rt.Adapter.ListModels(ctx)
		if err != nil {
			continue
		}
		ids := make([]string, 0, len(ms))
		for _, md := range ms {
			ids = append(ids, md.ID)
		}
		merge("(单账号)", ids, nil)
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

// ───────────────────────── 模型 ─────────────────────────

// apiModels 返回全部平台的模型目录（多平台合并，每项带来源 platform 字段）。
//
// 响应三层结构：
//   - models：扁平合并清单（含 platform 来源），旧前端照常渲染表格；
//   - platforms：按平台分组的视图，控制台据此做分类/筛选；
//   - platform：默认平台 ID，兼容只认单平台的旧客户端。
func (a *App) apiModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	all, perr := a.hub.ListModels(ctx)
	if perr != nil {
		writeJSON(w, map[string]any{"models": []any{}, "error": llm.Wrap(perr).Error()})
		return
	}

	type platformModels struct {
		ID     string              `json:"id"`
		Name   string              `json:"name"`
		Models []adapter.ModelInfo `json:"models"`
	}
	var platforms []platformModels
	for _, rt := range a.hub.Platforms() {
		ms, err := rt.Adapter.ListModels(ctx)
		if err != nil {
			continue
		}
		platforms = append(platforms, platformModels{ID: rt.ID, Name: rt.ID, Models: ms})
	}

	def := a.hub.DefaultPlatform()
	defID := ""
	if def != nil {
		defID = def.ID
	}
	writeJSON(w, map[string]any{
		"platform":  defID,
		"platforms": platforms,
		"models":    all,
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
	// Platforms 是多平台集成下每个平台的脱敏开关状态。
	Platforms []platformSanitize `json:"platforms"`
}

type platformSanitize struct {
	ID       string `json:"id"`
	Sanitize bool   `json:"sanitize"`
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
	def := a.hub.DefaultPlatform()
	out.Listen = a.cfg.Addr()
	out.Auth = a.cfg.Auth.APIKey != ""
	if def != nil {
		out.Upstream.Platform = def.ID
		out.Upstream.BaseURL = def.baseURL()
		if c, ok := def.Adapter.(adapter.Configurable); ok {
			out.Sanitize = c.Sanitize()
		} else {
			out.Sanitize = def.Cfg.Sanitize
		}
	}
	out.Upstream.StreamIdleTimeout = humanDuration(a.cfg.StreamIdleTimeout())
	out.Upstream.StreamTotalTimeout = humanDuration(a.cfg.StreamTotalTimeout())
	out.Upstream.RequestTimeout = humanDuration(a.cfg.RequestTimeout())
	out.Upstream.ModelCacheTTL = humanDuration(a.cfg.ModelCacheTTL())
	for _, rt := range a.hub.Platforms() {
		ps := platformSanitize{ID: rt.ID, Sanitize: rt.Cfg.Sanitize}
		if c, ok := rt.Adapter.(adapter.Configurable); ok {
			ps.Sanitize = c.Sanitize()
		}
		out.Platforms = append(out.Platforms, ps)
	}
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

type configPatch struct {
	Sanitize      *bool `json:"sanitize"`
	RefreshModels bool  `json:"refresh_models"`
}

// updateConfig 只开放「可以安全热更新」的项。
//
// 端口、上游地址这类改动需要重启，控制台不做假动作——避免用户以为改了却没生效。
// 多平台语义：带 ?platform= 只作用于该平台；不带则作用于全部平台
// （统一控制台里的全局开关就该作用于全局）。
func (a *App) updateConfig(w http.ResponseWriter, r *http.Request) {
	var patch configPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&patch); err != nil {
		http.Error(w, "请求体解析失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	applied := map[string]any{}

	targets := a.hub.Platforms()
	if id := r.URL.Query().Get("platform"); id != "" {
		rt := a.hub.Platform(id)
		if rt == nil {
			http.Error(w, "未知平台: "+id, http.StatusBadRequest)
			return
		}
		targets = []*PlatformRuntime{rt}
	}

	if patch.Sanitize != nil {
		appliedAny := false
		for _, rt := range targets {
			c, ok := rt.Adapter.(adapter.Configurable)
			if !ok {
				continue
			}
			c.SetSanitize(*patch.Sanitize)
			rt.Cfg.Sanitize = *patch.Sanitize
			appliedAny = true
		}
		if !appliedAny {
			http.Error(w, "所选平台均不支持运行期修改脱敏开关", http.StatusBadRequest)
			return
		}
		a.cfg.Upstream.Sanitize = *patch.Sanitize
		applied["sanitize"] = *patch.Sanitize
	}
	if patch.RefreshModels {
		n := 0
		for _, rt := range targets {
			if c, ok := rt.Adapter.(adapter.Configurable); ok {
				c.InvalidateModels()
				n++
			}
		}
		if n > 0 {
			applied["refresh_models"] = n
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
