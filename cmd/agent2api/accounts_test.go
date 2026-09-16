package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/config"
)

// sandboxHome 把 HOME/USERPROFILE 指向临时目录，使默认凭证路径可预测。
// 返回该临时 HOME。
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows：os.UserHomeDir 读的是它
	return home
}

// touchCred 在给定绝对路径上放一个空凭证文件（只参与路径筛选，内容无关）。
func touchCred(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"auth":{}}`), 0o600); err != nil {
		t.Fatalf("写入凭证失败: %v", err)
	}
}

// TestCollectCredentialPathsAutoDetect 守住单账号自动探测：
// 未配置 credential_path 时必须带上本机默认凭证位置，否则网关空池启动、
// 直接报 no_credential（曾因空字符串哨兵被去重函数吃掉而回归）。
func TestCollectCredentialPathsAutoDetect(t *testing.T) {
	sandboxHome(t)
	candidates := workbuddy.DefaultCredentialPaths()
	if len(candidates) == 0 {
		t.Skip("当前平台无默认凭证路径")
	}
	first := candidates[0]
	touchCred(t, first)

	got := collectCredentialPaths(config.Default())
	if len(got) != 1 || got[0] != first {
		t.Fatalf("自动探测未命中默认凭证: got %#v, want [%s]", got, first)
	}
}

// TestCollectCredentialPathsAutoDetectPrefersExisting 验证损坏候选之后的回退：
// 前面的候选也存在时，全部候选都应参与，交给号池逐个尝试。
func TestCollectCredentialPathsAutoDetectPrefersExisting(t *testing.T) {
	sandboxHome(t)
	candidates := workbuddy.DefaultCredentialPaths()
	if len(candidates) < 2 {
		t.Skip("当前平台默认凭证候选不足 2 个")
	}
	for _, p := range candidates[:2] {
		touchCred(t, p)
	}

	got := collectCredentialPaths(config.Default())
	if len(got) != 2 {
		t.Fatalf("应枚举全部存在的候选: got %#v", got)
	}
}

// TestCollectCredentialPathsExplicit 验证显式凭证优先且不被覆盖。
func TestCollectCredentialPathsExplicit(t *testing.T) {
	sandboxHome(t)
	candidates := workbuddy.DefaultCredentialPaths()
	for _, p := range candidates {
		touchCred(t, p)
	}

	cfg := config.Default()
	cfg.Upstream.CredentialPath = filepath.Join(t.TempDir(), "mine.json")
	touchCred(t, cfg.Upstream.CredentialPath)

	got := collectCredentialPaths(cfg)
	if len(got) != 1 || got[0] != cfg.Upstream.CredentialPath {
		t.Fatalf("显式凭证应优先且不叠加自动探测: got %#v", got)
	}
}

// TestCollectCredentialPathsAccountsDir 验证号池目录：按文件名排序、去重。
func TestCollectCredentialPathsAccountsDir(t *testing.T) {
	sandboxHome(t)
	dir := t.TempDir()
	for _, name := range []string{"b.json", "a.json", "note.txt", "sub"} {
		p := filepath.Join(dir, name)
		if name == "sub" {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatalf("创建子目录失败: %v", err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}

	cfg := config.Default()
	cfg.Upstream.AccountsDir = dir
	got := collectCredentialPaths(cfg)
	want := []string{filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")}
	if len(got) != len(want) {
		t.Fatalf("应只收录 *.json 且按名排序: got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序/筛选不符: got %#v, want %#v", got, want)
		}
	}
}
