// Package config 定义网关配置。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config 是网关配置。
type Config struct {
	Server   ServerConfig   `json:"server"`
	Upstream UpstreamConfig `json:"upstream"`
	Auth     AuthConfig     `json:"auth"`
	Log      LogConfig      `json:"log"`
	// MetricsFile 是指标落盘路径；空字符串表示不持久化（重启即清零）。
	// 配置为绝对或相对路径均可；相对路径相对进程工作目录解析。
	MetricsFile string `json:"metrics_file,omitempty"`
}

// ServerConfig 是 HTTP 服务配置。
type ServerConfig struct {
	Host            string `json:"host"`
	Port            int    `json:"port"`
	ReadTimeoutSec  int    `json:"read_timeout_sec"`
	WriteTimeoutSec int    `json:"write_timeout_sec"`
}

// UpstreamConfig 是上游配置。
type UpstreamConfig struct {
	// Platform 是单平台模式的上游平台（当前仅支持 workbuddy）。
	//
	// 空字符串（默认）表示「自动集成」：ResolvePlatforms 返回 autoPlatforms
	// （当前即 workbuddy），网关自动探测本机登录态。显式填写（配置文件或
	// -platform）则固定使用该平台。
	Platform string `json:"platform"`

	// Platforms 是「一个控制台集成多个上游平台」的声明式配置。
	//
	// 一旦非空，网关会在同一个进程里启动并管理列表里的每一个平台，
	// 客户端用 model 字段直接路由到对应平台（见 model_item_key / 路由说明）。
	// 每个平台的字段与下面单平台字段一一对应；缺省项回退到本结构体顶层同名项。
	//
	// 向后兼容：Platforms 为空时网关把单平台字段合成一个 PlatformConfig，
	// 行为与此前完全一致。
	Platforms []PlatformConfig `json:"platforms"`

	BaseURL string `json:"base_url"`

	// CredentialPath 为空时自动探测本机已登录凭证。
	CredentialPath string `json:"credential_path"`

	// AccountsDir 是多账号号池的凭证目录：目录下每个 *.json 视为一个账号。
	// 非空时与 CredentialPath 并存（CredentialPath 优先作为第一个账号），
	// 目录不存在或为空时自动退化为单账号模式。
	// 用法：agent2api login -out auths/a.json 逐个登录后重启网关。
	AccountsDir string `json:"accounts_dir"`

	// Sanitize 开启内容脱敏。接入 Claude Code / Codex 时必须开启。
	Sanitize bool `json:"sanitize"`

	StreamIdleTimeoutSec  int `json:"stream_idle_timeout_sec"`
	StreamTotalTimeoutSec int `json:"stream_total_timeout_sec"`
	RequestTimeoutSec     int `json:"request_timeout_sec"`
	ModelCacheTTLSec      int `json:"model_cache_ttl_sec"`
}

// PlatformConfig 是单个上游平台的配置。
//
// 字段含义与 UpstreamConfig 顶层单平台字段完全一致；差异只在作用范围——
// 这里只作用于某一个平台，而非全局。平台 ID 同时作为模型路由与控制台分组键。
type PlatformConfig struct {
	// ID 是平台标识（如 "workbuddy"）。与管理台、路由一一对应。
	ID string `json:"id"`
	// CredentialPath 该平台的凭证文件；为空时按平台自动探测本机登录态。
	CredentialPath string `json:"credential_path,omitempty"`
	// AccountsDir 该平台的多账号号池目录；非空时启用号池。
	AccountsDir string `json:"accounts_dir,omitempty"`
	// BaseURL 该平台上游地址；为空时用平台默认值。
	BaseURL string `json:"base_url,omitempty"`
	// Sanitize 该平台是否开启脱敏。
	Sanitize bool `json:"sanitize"`
	// 超时/缓存项：为 0 时回退到 UpstreamConfig 顶层同名项。
	StreamIdleTimeoutSec  int `json:"stream_idle_timeout_sec,omitempty"`
	StreamTotalTimeoutSec int `json:"stream_total_timeout_sec,omitempty"`
	RequestTimeoutSec     int `json:"request_timeout_sec,omitempty"`
	ModelCacheTTLSec      int `json:"model_cache_ttl_sec,omitempty"`
}

// autoPlatforms 是「自动集成」的候选清单（零配置时的默认行为）。
// 当前仅 workbuddy；新增平台适配器后在此追加即可。
// 顺序即默认平台优先级：列表第一个是默认平台，未知模型名透传给它。
var autoPlatforms = []PlatformConfig{
	{ID: "workbuddy"},
}

// ResolvePlatforms 把配置归一化为「实际要启动的平台列表」。
//
// 优先级：Platforms 列表 > 单 platform 字段 > 自动集成。
// 自动集成（零配置默认）返回全部内置平台，由装配层逐个探测本机登录态——
// 探测不到的平台跳过并告警，能登录哪个就集成哪个。
func (c Config) ResolvePlatforms() []PlatformConfig {
	if len(c.Upstream.Platforms) > 0 {
		return c.Upstream.Platforms
	}
	if c.Upstream.Platform != "" {
		return []PlatformConfig{{
			ID:                    c.Upstream.Platform,
			CredentialPath:        c.Upstream.CredentialPath,
			AccountsDir:           c.Upstream.AccountsDir,
			BaseURL:               c.Upstream.BaseURL,
			Sanitize:              c.Upstream.Sanitize,
			StreamIdleTimeoutSec:  c.Upstream.StreamIdleTimeoutSec,
			StreamTotalTimeoutSec: c.Upstream.StreamTotalTimeoutSec,
			RequestTimeoutSec:     c.Upstream.RequestTimeoutSec,
			ModelCacheTTLSec:      c.Upstream.ModelCacheTTLSec,
		}}
	}
	// 自动集成：平台的明细字段（凭证目录 / base_url / 超时）全部走
	// 各适配器内置默认；sanitize 继承顶层开关（默认开启，接 Claude Code 必需）。
	out := make([]PlatformConfig, len(autoPlatforms))
	for i, p := range autoPlatforms {
		out[i] = p
		out[i].Sanitize = c.Upstream.Sanitize
	}
	return out
}

// CloneForPlatform 返回一个只针对单个平台 pc 的配置副本：
// 用 pc 的明细覆盖顶层单平台字段，其余（Server / Auth / Log / Metrics）原样保留，
// 这样 buildAdapter / newPlatformAdapter 可以复用「读顶层字段」的旧逻辑无需改动。
func (c Config) CloneForPlatform(pc PlatformConfig) Config {
	c.Upstream.Platform = pc.ID
	if pc.CredentialPath != "" {
		c.Upstream.CredentialPath = pc.CredentialPath
	}
	if pc.AccountsDir != "" {
		c.Upstream.AccountsDir = pc.AccountsDir
	}
	if pc.BaseURL != "" {
		c.Upstream.BaseURL = pc.BaseURL
	}
	c.Upstream.Sanitize = pc.Sanitize
	if pc.StreamIdleTimeoutSec > 0 {
		c.Upstream.StreamIdleTimeoutSec = pc.StreamIdleTimeoutSec
	}
	if pc.StreamTotalTimeoutSec > 0 {
		c.Upstream.StreamTotalTimeoutSec = pc.StreamTotalTimeoutSec
	}
	if pc.RequestTimeoutSec > 0 {
		c.Upstream.RequestTimeoutSec = pc.RequestTimeoutSec
	}
	if pc.ModelCacheTTLSec > 0 {
		c.Upstream.ModelCacheTTLSec = pc.ModelCacheTTLSec
	}
	return c
}

// AuthConfig 是网关侧鉴权配置。
type AuthConfig struct {
	// APIKey 非空时，客户端必须提供匹配的凭据。
	// Chat/Responses 用 Authorization: Bearer；Anthropic 用 x-api-key。
	APIKey string `json:"api_key"`
}

// LogConfig 是日志配置。
type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

// Default 返回默认配置。
func Default() Config {
	return Config{
		Server: ServerConfig{
			Host:            "127.0.0.1",
			Port:            8787,
			ReadTimeoutSec:  60,
			WriteTimeoutSec: 0, // 流式响应不能有写超时
		},
		Upstream: UpstreamConfig{
			// Platform 留空 = 自动集成：启动时逐个探测全部内置平台，
			// 能登录哪个就集成哪个。显式 -platform 或配置文件里写 platform
			// 则退回单平台模式。
			Platform:              "",
			Sanitize:              true,
			StreamIdleTimeoutSec:  120,
			StreamTotalTimeoutSec: 1800,
			RequestTimeoutSec:     60,
			ModelCacheTTLSec:      1800,
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
}

// Load 从文件加载配置，缺失字段用默认值填充。
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		cfg.applyEnv()
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.applyEnv()
			return cfg, nil
		}
		return cfg, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置文件失败: %w", err)
	}
	cfg.applyEnv()
	return cfg, nil
}

// applyEnv 允许环境变量覆盖关键项。
func (c *Config) applyEnv() {
	if v := os.Getenv("AGENT2API_API_KEY"); v != "" {
		c.Auth.APIKey = v
	}
	if v := os.Getenv("AGENT2API_PORT"); v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err == nil && p > 0 {
			c.Server.Port = p
		}
	}
	if v := os.Getenv("AGENT2API_HOST"); v != "" {
		c.Server.Host = v
	}
	if v := os.Getenv("AGENT2API_CREDENTIAL_PATH"); v != "" {
		c.Upstream.CredentialPath = v
	}
	if v := os.Getenv("AGENT2API_BASE_URL"); v != "" {
		c.Upstream.BaseURL = v
	}
}

// Addr 返回监听地址。
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

// StreamIdleTimeout 返回流式空闲超时。
func (c Config) StreamIdleTimeout() time.Duration {
	if c.Upstream.StreamIdleTimeoutSec <= 0 {
		return 120 * time.Second
	}
	return time.Duration(c.Upstream.StreamIdleTimeoutSec) * time.Second
}

// StreamTotalTimeout 返回流式总时长上限。
func (c Config) StreamTotalTimeout() time.Duration {
	if c.Upstream.StreamTotalTimeoutSec <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(c.Upstream.StreamTotalTimeoutSec) * time.Second
}

// RequestTimeout 返回非流式请求超时。
func (c Config) RequestTimeout() time.Duration {
	if c.Upstream.RequestTimeoutSec <= 0 {
		return 60 * time.Second
	}
	return time.Duration(c.Upstream.RequestTimeoutSec) * time.Second
}

// ModelCacheTTL 返回模型清单缓存时长。
func (c Config) ModelCacheTTL() time.Duration {
	if c.Upstream.ModelCacheTTLSec <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(c.Upstream.ModelCacheTTLSec) * time.Second
}
