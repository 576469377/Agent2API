package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"agent2api/internal/adapter"
	"agent2api/internal/llm"
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
