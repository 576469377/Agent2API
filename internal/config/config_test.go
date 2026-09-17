package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultValues(t *testing.T) {
	cfg := Default()
	if cfg.Server.Host != "127.0.0.1" || cfg.Server.Port != 8787 {
		t.Fatalf("默认监听错误: %+v", cfg.Server)
	}
	if !cfg.Upstream.Sanitize {
		t.Fatal("脱敏默认必须开启（接 Claude Code / Codex 必需）")
	}
	// 零配置 = 自动集成：默认平台字段为空，ResolvePlatforms 返回内置平台列表
	//（当前仅 workbuddy）。
	if cfg.Upstream.Platform != "" {
		t.Fatalf("默认 platform 应为空（自动集成）, got %q", cfg.Upstream.Platform)
	}
	pls := cfg.ResolvePlatforms()
	if len(pls) != 1 || pls[0].ID != "workbuddy" {
		t.Fatalf("自动集成应返回 workbuddy, got %+v", pls)
	}
	if !pls[0].Sanitize {
		t.Fatal("自动集成应继承顶层脱敏开关（默认开启）")
	}
}

func TestExplicitPlatformStaysSingle(t *testing.T) {
	cfg := Default()
	cfg.Upstream.Platform = "workbuddy"
	pls := cfg.ResolvePlatforms()
	if len(pls) != 1 || pls[0].ID != "workbuddy" {
		t.Fatalf("显式 platform 应回退单平台, got %+v", pls)
	}
}

func TestLoadMissingFileUsesDefaultAndEnv(t *testing.T) {
	t.Setenv("AGENT2API_PORT", "9001")
	t.Setenv("AGENT2API_API_KEY", "k-test")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("缺失配置文件应静默回退默认: %v", err)
	}
	if cfg.Server.Port != 9001 || cfg.Auth.APIKey != "k-test" {
		t.Fatalf("env 覆盖未生效: %+v", cfg)
	}
}

func TestLoadFileOverridesDefault(t *testing.T) {
	// 隔离外部环境：本机可能设了 AGENT2API_*，不清理会污染断言。
	t.Setenv("AGENT2API_PORT", "")
	t.Setenv("AGENT2API_API_KEY", "")
	t.Setenv("AGENT2API_HOST", "")
	t.Setenv("AGENT2API_CREDENTIAL_PATH", "")
	t.Setenv("AGENT2API_BASE_URL", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{"server":{"port":7777},"upstream":{"sanitize":false,"base_url":"https://example.com"}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 7777 {
		t.Fatalf("port=%d, want 7777", cfg.Server.Port)
	}
	if cfg.Upstream.Sanitize {
		t.Fatal("配置文件应能关闭脱敏")
	}
	if cfg.Upstream.BaseURL != "https://example.com" {
		t.Fatalf("base_url=%q", cfg.Upstream.BaseURL)
	}
	// 未覆盖字段必须保留默认值（Load 从 Default() 起步再 Unmarshal）。
	if cfg.Server.Host != "127.0.0.1" {
		t.Fatalf("未覆盖字段丢了默认值: host=%q", cfg.Server.Host)
	}
}

func TestLoadBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("坏配置文件应报错而不是静默使用默认")
	}
}

func TestAddrAndDurations(t *testing.T) {
	cfg := Default()
	if got := cfg.Addr(); got != "127.0.0.1:8787" {
		t.Fatalf("Addr=%q", got)
	}
	// 非法/零值配置必须回退安全默认，而不是产生 0 时长（会把超时守卫废掉）。
	cfg.Upstream.RequestTimeoutSec = 0
	if cfg.RequestTimeout() <= 0 {
		t.Fatal("RequestTimeout 零值应回退默认")
	}
	cfg.Upstream.StreamIdleTimeoutSec = -1
	if cfg.StreamIdleTimeout() <= 0 {
		t.Fatal("StreamIdleTimeout 负值应回退默认")
	}
	cfg.Upstream.StreamTotalTimeoutSec = 0
	if cfg.StreamTotalTimeout() <= 0 {
		t.Fatal("StreamTotalTimeout 零值应回退默认")
	}
	cfg.Upstream.ModelCacheTTLSec = 0
	if cfg.ModelCacheTTL() <= 0 {
		t.Fatal("ModelCacheTTL 零值应回退默认")
	}
}
