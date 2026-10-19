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
	if len(pls) != 2 || pls[0].ID != "workbuddy" || pls[1].ID != "workbuddy-global" {
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

func TestOpenRouterCredentialsStayWithinPlatform(t *testing.T) {
	cfg := Default()
	cfg.Upstream.Platform = "openrouter"
	cfg.Upstream.APIKey = "test-upstream"
	cfg.Upstream.APIKeyEnv = "TEST_ROUTER_KEY"
	cfg.Auth.APIKey = "test-gateway"
	pc := cfg.ResolvePlatforms()[0]
	clone := cfg.CloneForPlatform(pc)
	if clone.Upstream.APIKey != "test-upstream" || clone.Upstream.APIKeyEnv != "TEST_ROUTER_KEY" || clone.Auth.APIKey != "test-gateway" {
		t.Fatal("single platform lost upstream credentials or gateway authentication")
	}
	cn := cfg.CloneForPlatform(PlatformConfig{ID: "workbuddy"})
	if cn.Upstream.APIKey != "" || cn.Upstream.APIKeyEnv != "" {
		t.Fatal("OpenRouter key leaked into another platform")
	}
	other := cfg.CloneForPlatform(PlatformConfig{ID: "openrouter", APIKeyEnv: "SECOND_ROUTER_KEY"})
	if other.Upstream.APIKey != "" || other.Upstream.APIKeyEnv != "SECOND_ROUTER_KEY" {
		t.Fatal("platform key override failed")
	}
}

func TestDirectProvidersDoNotInheritOtherPlatformEndpoints(t *testing.T) {
	cfg := Default()
	cfg.Upstream.BaseURL = "https://workbuddy.example.invalid"
	cfg.Upstream.CredentialPath = "other-platform.json"
	cfg.Upstream.AccountsDir = "other-accounts"
	for _, id := range []string{"cursor", "openrouter"} {
		clone := cfg.CloneForPlatform(PlatformConfig{ID: id})
		if clone.Upstream.BaseURL != "" || clone.Upstream.CredentialPath != "" || clone.Upstream.AccountsDir != "" {
			t.Fatalf("%s inherited another provider's settings", id)
		}
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

// TestConcurrencyDefaultAndPlatformOverride 覆盖并发上限的默认值、
// 平台级覆盖：默认不限并发，仍支持用户显式设置正数上限。
func TestConcurrencyDefaultAndPlatformOverride(t *testing.T) {
	cfg := Default()
	if cfg.Upstream.MaxConcurrencyPerAccount != 0 {
		t.Fatalf("默认每账号应不限并发, got %d", cfg.Upstream.MaxConcurrencyPerAccount)
	}
	// 自动集成路径继承顶层值。
	if pls := cfg.ResolvePlatforms(); pls[0].MaxConcurrencyPerAccount != 0 {
		t.Fatalf("自动集成不应覆盖顶层值, got %+v", pls[0])
	}
	if got := cfg.CloneForPlatform(cfg.ResolvePlatforms()[0]); got.Upstream.MaxConcurrencyPerAccount != 0 {
		t.Fatalf("未声明平台级上限时应默认不限并发, got %d", got.Upstream.MaxConcurrencyPerAccount)
	}
	// 平台级显式覆盖。
	cfg.Upstream.Platforms = []PlatformConfig{{ID: "workbuddy", Sanitize: true, MaxConcurrencyPerAccount: 9}}
	got := cfg.CloneForPlatform(cfg.Upstream.Platforms[0])
	if got.Upstream.MaxConcurrencyPerAccount != 9 {
		t.Fatalf("平台级上限应覆盖顶层, got %d", got.Upstream.MaxConcurrencyPerAccount)
	}
	// 显式单平台模式把顶层值带进 PlatformConfig（供装配层统一读取）。
	cfg2 := Default()
	cfg2.Upstream.Platform = "workbuddy"
	cfg2.Upstream.MaxConcurrencyPerAccount = 7
	pls := cfg2.ResolvePlatforms()
	if pls[0].MaxConcurrencyPerAccount != 7 {
		t.Fatalf("单平台模式应带上顶层上限, got %+v", pls[0])
	}
	// 0 表示不限并发——允许显式关闭。
	cfg3 := Default()
	cfg3.Upstream.MaxConcurrencyPerAccount = 0
	if got := cfg3.CloneForPlatform(cfg3.ResolvePlatforms()[0]); got.Upstream.MaxConcurrencyPerAccount != 0 {
		t.Fatalf("0 应表示不限并发, got %d", got.Upstream.MaxConcurrencyPerAccount)
	}
}

// TestConcurrencyFromJSON 验证缺省不限、显式 0 与已有正数配置。
func TestConcurrencyFromJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"omitted", `{"upstream":{}}`, 0},
		{"unlimited", `{"upstream":{"max_concurrency_per_account":0}}`, 0},
		{"explicit_limit", `{"upstream":{"max_concurrency_per_account":4}}`, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, platform := range cfg.ResolvePlatforms() {
				if got := cfg.CloneForPlatform(platform).Upstream.MaxConcurrencyPerAccount; got != tc.want {
					t.Fatalf("platform %s: max concurrency=%d, want %d", platform.ID, got, tc.want)
				}
			}
		})
	}
}

// TestAliasesFromJSON 验证别名表能按文档里的写法被解析出来。
func TestAliasesFromJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	body := `{
	  "models": {
	    "aliases": {
	      "claude-sonnet-4-5-20250929": "glm-5.3",
	      "gpt-4o": "workbuddy/deepseek-v4-pro"
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if len(cfg.Models.Aliases) != 2 {
		t.Fatalf("别名条数不对: %+v", cfg.Models.Aliases)
	}
	if cfg.Models.Aliases["gpt-4o"] != "workbuddy/deepseek-v4-pro" {
		t.Fatalf("平台限定别名未解析: %+v", cfg.Models.Aliases)
	}
}
