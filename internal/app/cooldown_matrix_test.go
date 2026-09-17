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

// TestMatrixShowsModelLevelCooldowns 复现用户实测场景：
// 账号 A 撞「额度耗尽」（14018）、账号 B 撞「频率限制」，两者都附带了上游的
// **精确重置时刻**（新调度策略下唯一会冷却的情况——无精确时刻的失败一律
// 不锁定、只换号），冷却都是**模型级**，矩阵端点应在对应格子暴露剩余时间
// ——而不是整号标一个倒计时污染整列。
// 注意：新策略下无精确时刻的 14018 不再冷却；这里附上精确时刻以覆盖
// 「模型级冷却在矩阵上的展示」这一纯展示逻辑。
func TestMatrixShowsModelLevelCooldowns(t *testing.T) {
	pool := adapter.NewPool("wb", nil)
	// 账号 A：额度耗尽（14018）+ 精确重置时刻 → 模型级冷却
	pool.Add("acct-a.json", &quotaAd{})
	// 账号 B：频率限制（模型级）+ 精确重置时刻
	pool.Add("acct-b.json", &freqAd{limitModel: "hy4-preview-f"})

	// 请求 hy4-preview-f 直到两个账号都进入冷却（最多 50 次）。
	for i := 0; i < 50; i++ {
		_, _ = pool.Stream(context.Background(), llm.RequestMessages{Model: "hy4-preview-f"})
		aCooled := false
		for _, st := range pool.Statuses() {
			if st.Label == "acct-a.json" && st.ModelCooldowns["hy4-preview-f"] > 0 {
				aCooled = true
			}
		}
		bCooled := false
		for _, st := range pool.Statuses() {
			if st.Label == "acct-b.json" && st.ModelCooldowns["hy4-preview-f"] > 0 {
				bCooled = true
			}
		}
		if aCooled && bCooled {
			break
		}
	}

	// 断言 Statuses：两个账号的 hy4-preview-f 都有模型级冷却记录。
	sts := pool.Statuses()
	byLabel := map[string]adapter.AccountStatus{}
	for _, st := range sts {
		byLabel[st.Label] = st
	}
	// A：模型级冷却 → 整号仍健康（免费模型可继续用），仅 hy4-preview-f 被冷。
	if !byLabel["acct-a.json"].Healthy {
		t.Fatal("额度耗尽是模型级冷却，整号应保持健康")
	}
	if byLabel["acct-a.json"].ModelCooldowns["hy4-preview-f"] <= 0 {
		t.Fatalf("A 应有 hy4-preview-f 的模型级冷却: %+v", byLabel["acct-a.json"].ModelCooldowns)
	}
	// B：模型级冷却
	if byLabel["acct-b.json"].ModelCooldowns["hy4-preview-f"] <= 0 {
		t.Fatalf("B 应有 hy4-preview-f 的模型级冷却: %+v", byLabel["acct-b.json"].ModelCooldowns)
	}

	// 端到端：矩阵端点应在对应格子暴露两者的冷却（不进 account_cooldowns）。
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
		Cooldowns        map[string]map[string]int `json:"cooldowns"`
		AccountCooldowns map[string]int            `json:"account_cooldowns"`
		Matrix           map[string][]string       `json:"matrix"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Matrix["acct-a.json"]; !ok {
		t.Fatal("矩阵应有 acct-a")
	}
	// A 的 hy4-preview-f 应带模型级冷却剩余时间。
	if out.Cooldowns["acct-a.json"]["hy4-preview-f"] <= 0 {
		t.Fatalf("hy4-preview-f 应带模型级冷却剩余时间: %+v", out.Cooldowns)
	}
	// B：只有模型级冷却的那一个模型，不波及其他模型。
	bCool := out.Cooldowns["acct-b.json"]
	if bCool["hy4-preview-f"] <= 0 {
		t.Fatalf("B 应有模型级冷却: %+v", bCool)
	}
	if len(bCool) != 1 {
		t.Fatalf("B 只有模型级冷却，不应波及其他模型: %+v", bCool)
	}
	// 没有任何账号级（整号）冷却。
	if len(out.AccountCooldowns) != 0 {
		t.Fatalf("模型级冷却不应进 account_cooldowns: %+v", out.AccountCooldowns)
	}
}

type quotaAd struct{}

func (f *quotaAd) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	fl := llm.NewFailure("upstream_14018", "额度已用尽，请购买加量包", nil)
	fl.QuotaExhausted = true
	fl.RateLimited = true
	// 新调度策略：只有精确重置时刻才会冷却；附上以触发模型级冷却。
	fl.RetryAfterSeconds = 60
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
		// 新调度策略：只有精确重置时刻才会冷却；附上以触发模型级冷却。
		fl.RetryAfterSeconds = 60
		return nil, fl
	}
	return &mxStream{}, nil
}
func (f *freqAd) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return []adapter.ModelInfo{{ID: "hy4-preview-f"}, {ID: "model-y"}}, nil
}
func (f *freqAd) Name() string { return "wb" }
