package app

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

// TestMatrixShowsAccountLevelCooldown 复现用户实测场景：
// 账号 A 撞「额度耗尽」（账号级）、账号 B 撞「频率限制」（模型级），
// 矩阵端点应把两者的冷却剩余时间都暴露出来——
// 修复前账号级冷却不进矩阵，用户看到 ✓ 一试才知道整号被冷。
func TestMatrixShowsAccountLevelCooldown(t *testing.T) {
	pool := adapter.NewPool("wb", nil)
	// 账号 A：额度耗尽（14018）→ 应触发账号级冷却
	pool.Add("acct-a.json", &quotaAd{})
	// 账号 B：频率限制（模型级）
	pool.Add("acct-b.json", &freqAd{limitModel: "hy4-preview-f"})

	// 请求 hy4-preview-f 直到两个账号都进入冷却（最多 50 次）。
	for i := 0; i < 50; i++ {
		_, _ = pool.Stream(context.Background(), llm.RequestMessages{Model: "hy4-preview-f"})
		pCooled := false
		for _, st := range pool.Statuses() {
			if st.Reason == "quota_exhausted" {
				pCooled = true
			}
		}
		bCooled := false
		for _, st := range pool.Statuses() {
			if st.Label == "acct-b.json" && st.ModelCooldowns["hy4-preview-f"] > 0 {
				bCooled = true
			}
		}
		if pCooled && bCooled {
			break
		}
	}

	// 断言 Statuses：两个账号的 hy4-preview-f 都有冷却记录
	sts := pool.Statuses()
	byLabel := map[string]adapter.AccountStatus{}
	for _, st := range sts {
		byLabel[st.Label] = st
	}
	// A：账号级冷却 → 所有模型（含 hy4-preview-f）应被标记
	if byLabel["acct-a.json"].Healthy {
		t.Fatal("额度耗尽后账号应不健康")
	}
	// B：模型级冷却
	if byLabel["acct-b.json"].ModelCooldowns["hy4-preview-f"] <= 0 {
		t.Fatalf("B 应有 hy4-preview-f 的模型级冷却: %+v", byLabel["acct-b.json"].ModelCooldowns)
	}

	// 端到端：矩阵端点应同时暴露两者的冷却
	cfg := config.Default()
	cfg.MetricsFile = ""
	logger := log.New(io.Discard, "", 0)
	hub, err := NewHub([]*PlatformRuntime{{ID: "wb", Adapter: pool}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	a := New(cfg, hub, logger)
	w := httptest.NewRecorder()
	a.apiAccountModels(w, httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil))

	var out struct {
		Cooldowns map[string]map[string]int `json:"cooldowns"`
		Matrix    map[string][]string       `json:"matrix"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// A：账号级冷却 → 全部模型带冷却
	aCool := out.Cooldowns["acct-a.json"]
	if len(aCool) == 0 {
		t.Fatalf("账号级冷却应展开到全部模型: %+v", out.Cooldowns)
	}
	if _, ok := out.Matrix["acct-a.json"]; !ok {
		t.Fatal("矩阵应有 acct-a")
	}
	// A 的 hy4-preview-f 应带账号级冷却剩余时间
	if aCool["hy4-preview-f"] <= 0 {
		t.Fatalf("hy4-preview-f 应带账号级冷却剩余时间: %+v", aCool)
	}
	// B：只有模型级冷却的那一个模型
	bCool := out.Cooldowns["acct-b.json"]
	if bCool["hy4-preview-f"] <= 0 {
		t.Fatalf("B 应有模型级冷却: %+v", bCool)
	}
	if len(bCool) != 1 {
		t.Fatalf("B 只有模型级冷却，不应波及其他模型: %+v", bCool)
	}
}

type quotaAd struct{}

func (f *quotaAd) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	fl := llm.NewFailure("upstream_14018", "额度已用尽，请购买加量包", nil)
	fl.QuotaExhausted = true
	fl.RateLimited = true
	return nil, fl
}
func (f *quotaAd) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return []adapter.ModelInfo{{ID: "hy4-preview-f"}, {ID: "model-y"}}, nil
}
func (f *quotaAd) Name() string { return "wb" }

type freqAd struct{ limitModel string }

func (f *freqAd) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	if req.Model == f.limitModel {
		fl := llm.NewFailure("rate_limited", "您的使用量已超出频率限制", nil)
		fl.RateLimited = true
		return nil, fl
	}
	return &mxStream{}, nil
}
func (f *freqAd) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return []adapter.ModelInfo{{ID: "hy4-preview-f"}, {ID: "model-y"}}, nil
}
func (f *freqAd) Name() string { return "wb" }
