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

// TestMatrixExposesModelCooldowns 验证矩阵端点暴露「账号 × 模型」的冷却剩余秒数。
//
// 矩阵页要能区分三态：支持 ✓ / 不支持 · / 限流冷却中（带倒计时）。
// 最后一种依赖这里的 cooldowns 字段——没有它，用户会以为某个「支持但被限」
// 的组合坏了。
func TestMatrixExposesModelCooldowns(t *testing.T) {
	pool := adapter.NewPool("wb", nil)
	pool.Add("acct-a.json", &mxAd{limitModel: "model-x"})
	pool.Add("acct-b.json", &mxAd{})

	// 触发 acct-a 上 model-x 的限流冷却。
	// 注意：亲和会让成功的会话粘住账号（先选中 b 就一直 b），所以要
	// 用**不同 SessionKey 且清空亲和**的方式强制命中 a——最直接的办法
	// 是直接往池里塞一个使用 a 的请求直到 a 被冷却。
	for i := 0; i < 50; i++ {
		_, _ = pool.Stream(context.Background(), llm.RequestMessages{Model: "model-x"})
		aCooled := false
		for _, st := range pool.Statuses() {
			if st.Label == "acct-a.json" && st.ModelCooldowns["model-x"] > 0 {
				aCooled = true
			}
		}
		if aCooled {
			break
		}
	}

	// 用与生产相同的方式构造 App（hub 包号池）。
	cfg := config.Default()
	cfg.MetricsFile = ""
	logger := log.New(io.Discard, "", 0)
	hub, err := NewHub([]*PlatformRuntime{{ID: "wb", Adapter: pool}}, logger)
	if err != nil {
		t.Fatalf("构造 hub: %v", err)
	}
	a := New(cfg, hub, logger)

	w := httptest.NewRecorder()
	a.apiAccountModels(w, httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Accounts  []string                  `json:"accounts"`
		Matrix    map[string][]string       `json:"matrix"`
		Cooldowns map[string]map[string]int `json:"cooldowns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v (%s)", err, w.Body.String())
	}
	t.Logf("accounts=%v cooldowns=%v", out.Accounts, out.Cooldowns)
	if out.Cooldowns["acct-a.json"]["model-x"] <= 0 {
		t.Fatalf("应暴露 acct-a 上 model-x 的冷却: %+v", out.Cooldowns)
	}
}

type mxAd struct {
	limitModel string
}

func (f *mxAd) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	if f.limitModel != "" && req.Model == f.limitModel {
		fl := llm.NewFailure("rate_limited", "quota", nil)
		fl.RateLimited = true
		return nil, fl
	}
	return &mxStream{}, nil
}

type mxStream struct{}

func (s *mxStream) Recv(context.Context) (llm.ResponseEvent, error) {
	return llm.ResponseEvent{}, llm.ErrStreamDone
}

func (f *mxAd) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return []adapter.ModelInfo{{ID: "model-x"}, {ID: "model-y"}}, nil
}
func (f *mxAd) Name() string { return "wb" }
