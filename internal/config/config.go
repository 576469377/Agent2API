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
	// Platform 是当前接入的平台，目前只支持 workbuddy。
	Platform string `json:"platform"`
	BaseURL  string `json:"base_url"`

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
			Platform:              "workbuddy",
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
