package app

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
)

type clientUIFixture struct {
	fakeAdapter
	platform string
}

func (*clientUIFixture) InvalidateModels() {}
func (*clientUIFixture) SetSanitize(bool)  {}
func (*clientUIFixture) Sanitize() bool    { return false }

func (f *clientUIFixture) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	zero := 0.0
	source := "upstream_config"
	if f.platform == "openrouter" {
		source = "openrouter_models"
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	return []adapter.ModelInfo{
		{ID: "first-free", DisplayName: "First free model", ContextTokens: 128000, MaxOutputTokens: 32000, SupportsTools: true, Pricing: &adapter.ModelPricing{Status: "free", Source: source, CreditMultiplier: &zero, ObservedAt: stamp}},
		{ID: "second-free", DisplayName: "Second free model", ContextTokens: 200000, MaxOutputTokens: 64000, SupportsTools: true, Pricing: &adapter.ModelPricing{Status: "free", Source: source, CreditMultiplier: &zero, ObservedAt: stamp}},
		{ID: "paid", ContextTokens: 200000, MaxOutputTokens: 64000, Pricing: &adapter.ModelPricing{Status: "credits", Source: source, ObservedAt: stamp}},
	}, nil
}

// 人工浏览器验收复用真实管理端点；全部文件位于测试临时目录，默认不启动服务。
func TestClientConsoleUIFixture(t *testing.T) {
	if os.Getenv("AGENT2API_CLIENT_UI_TEST") != "1" {
		t.Skip("manual browser fixture")
	}
	dir := t.TempDir()
	hub, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: &clientUIFixture{platform: "workbuddy"}}, {ID: "openrouter", Adapter: &clientUIFixture{platform: "openrouter"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MetricsFile, cfg.Server.Host, cfg.Server.Port = "", "127.0.0.1", 18787
	a := New(cfg, hub, nil)
	a.piPath, a.ccSwitchPath = filepath.Join(dir, "pi", "models.json"), filepath.Join(dir, "cc-switch.db")
	a.zcodePath = filepath.Join(dir, "provider_config.json")
	os.WriteFile(a.zcodePath, []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"other","enabled":true}]},"defaultModelSelection":{"providerId":"other","modelId":"keep"}}}`), 0o600)
	db, err := sql.Open("sqlite", a.ccSwitchPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE providers(id TEXT NOT NULL,app_type TEXT NOT NULL,name TEXT NOT NULL,settings_config TEXT NOT NULL,meta TEXT NOT NULL DEFAULT '{}',is_current BOOLEAN NOT NULL DEFAULT 0,in_failover_queue BOOLEAN NOT NULL DEFAULT 0,created_at INTEGER,sort_index INTEGER,category TEXT,notes TEXT,PRIMARY KEY(id,app_type));CREATE TABLE provider_endpoints(id INTEGER PRIMARY KEY,provider_id TEXT,app_type TEXT,url TEXT);CREATE TABLE history(message TEXT);INSERT INTO providers(id,app_type,name,settings_config,is_current) VALUES('other','claude','Existing provider','{}',1);INSERT INTO history VALUES('preserve history');`)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:18787")
	if err != nil {
		t.Fatal(err)
	}
	done, once := make(chan struct{}), sync.Once{}
	mux := http.NewServeMux()
	mux.Handle("/", a.Handler())
	mux.HandleFunc("/_fixture/finish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !localClient(r) || r.Header.Get("X-Agent2API-Local-Action") != "finish-fixture" {
			http.Error(w, "fixture only", 403)
			return
		}
		once.Do(func() { close(done) })
		fmt.Fprint(w, "finished")
	})
	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	defer server.Close()
	fmt.Println("CLIENT_UI_FIXTURE_READY http://127.0.0.1:18787/#connection")
	select {
	case <-done:
	case <-time.After(12 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	var current, history string
	db.QueryRow("SELECT id FROM providers WHERE is_current=1").Scan(&current)
	db.QueryRow("SELECT message FROM history").Scan(&history)
	if current != "other" || history != "preserve history" {
		t.Fatal("existing client data changed")
	}
}
