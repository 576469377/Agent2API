package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

// recordingAdapter 记录收到的模型名，并返回一段最小可用的文本流。
// 用于验证别名确实改写了「发往上游的模型名」与目标平台。
type recordingAdapter struct {
	gotModel string
	gotReq   llm.RequestMessages
}

func (r *recordingAdapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	r.gotModel = req.Model
	r.gotReq = req
	return &eventStream{events: []llm.ResponseEvent{
		{Type: llm.EventStart, Model: req.Model},
		{Type: llm.EventTextStart, ContentIndex: 0},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "ok"},
		{Type: llm.EventTextEnd, ContentIndex: 0},
		{Type: llm.EventDone, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}},
	}}, nil
}

func (r *recordingAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) { return nil, nil }
func (r *recordingAdapter) Name() string                                            { return "rec" }

func newAliasHub(t *testing.T, aliases map[string]string) (*Hub, *recordingAdapter, *recordingAdapter) {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	wb, other := &recordingAdapter{}, &recordingAdapter{}
	hub, err := NewHub([]*PlatformRuntime{
		{ID: "wb", Adapter: wb},
		{ID: "other", Adapter: other},
	}, logger)
	if err != nil {
		t.Fatalf("构造 hub: %v", err)
	}
	hub.SetAliases(aliases)
	return hub, wb, other
}

// TestResolveAliasForms 覆盖别名的三种写法与容错：
// 同平台映射、平台限定映射、平台名写错时退化为普通模型名（不能让一个笔误
// 把网关变成不可用）、以及自我映射被忽略。
func TestResolveAliasForms(t *testing.T) {
	hub, _, _ := newAliasHub(t, map[string]string{
		"claude-sonnet-4-5": "glm-5.3",               // 同平台：只换模型
		"gpt-4o":            "other/deepseek-v4-pro", // 平台限定：换平台 + 换模型
		"typo":              "nope/some-model",       // 平台不存在：按普通模型名
		"same":              "same",                  // 自我映射：忽略
	})

	cases := []struct {
		in       string
		wantMdl  string
		wantPlat string
	}{
		{"claude-sonnet-4-5", "glm-5.3", ""},
		{"gpt-4o", "deepseek-v4-pro", "other"},
		{"typo", "nope/some-model", ""},
		{"same", "same", ""},
		{"unknown-model", "unknown-model", ""},
	}
	for _, c := range cases {
		gotMdl, gotPlat := hub.ResolveAlias(c.in)
		if gotMdl != c.wantMdl || gotPlat != c.wantPlat {
			t.Errorf("ResolveAlias(%q) = (%q,%q)，want (%q,%q)",
				c.in, gotMdl, gotPlat, c.wantMdl, c.wantPlat)
		}
	}
}

// TestAliasRoutesAndRewritesModel 是端到端回归：命中别名的请求必须
// ① 落到别名指定的平台，② 以目标模型名发给上游——而不是把别名原样透传。
func TestAliasRoutesAndRewritesModel(t *testing.T) {
	hub, wb, other := newAliasHub(t, map[string]string{
		"gpt-4o": "other/deepseek-v4-pro",
	})
	cfg := config.Default()
	cfg.MetricsFile = ""
	a := New(cfg, hub, log.New(io.Discard, "", 0))

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	a.serve(w, req, chatProtocol{})

	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("别名请求应成功, got %d: %s", w.Result().StatusCode, w.Body.String())
	}
	if other.gotModel != "deepseek-v4-pro" {
		t.Fatalf("请求应以目标模型名发往上游, got %q", other.gotModel)
	}
	if wb.gotModel != "" {
		t.Fatalf("别名指定了平台，不应落到默认平台, got %q", wb.gotModel)
	}
	// 指标里要能回答「这条 gpt-4o 实际用了什么模型」。
	snap := a.metrics.Snapshot()
	if len(snap.Recent) == 0 || snap.Recent[0].ModelRequested != "gpt-4o" {
		t.Fatalf("应记录原始请求模型, got %+v", snap.Recent)
	}
	if snap.Recent[0].Model != "deepseek-v4-pro" {
		t.Fatalf("应记录实际模型, got %+v", snap.Recent[0])
	}
}

// TestAliasesListedInCatalog 验证别名会出现在模型目录里：
// 客户端（Claude Code 等）通常只认目录中出现过的模型名。
func TestAliasesListedInCatalog(t *testing.T) {
	hub, _, _ := newAliasHub(t, map[string]string{"claude-sonnet-4-5": "glm-5.3"})
	models, err := hub.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range models {
		if m.ID == "claude-sonnet-4-5" {
			found = true
			if m.Platform != "(别名)" {
				t.Fatalf("别名项应标注来源, got %q", m.Platform)
			}
			if !strings.Contains(m.DisplayName, "glm-5.3") {
				t.Fatalf("别名项应显示目标模型, got %q", m.DisplayName)
			}
		}
	}
	if !found {
		t.Fatal("别名未出现在模型目录中")
	}
}

// TestSessionKeyPrefersExplicitHeader 验证显式会话头优先于 body 推断：
// 客户端明说会话身份时最可信（sub2api 用 session_id 头做粘性会话）。
func TestSessionKeyPrefersExplicitHeader(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"u-1"},"messages":[{"role":"user","content":"hi"}]}`)

	h := http.Header{}
	h.Set("session_id", "abc") // 下划线形式（Nginx 默认会丢弃，见 affinity.go 注释）
	if got := sessionKeyOf(body, h); got != "s:abc" {
		t.Fatalf("应优先用 session_id 头, got %q", got)
	}
	h = http.Header{}
	h.Set("X-Session-Id", "def")
	if got := sessionKeyOf(body, h); got != "s:def" {
		t.Fatalf("应识别 X-Session-Id, got %q", got)
	}
	// 无显式头时回落到 body 里的稳定字段。
	if got := sessionKeyOf(body, http.Header{}); got != "u:u-1" {
		t.Fatalf("无会话头时应回落 metadata.user_id, got %q", got)
	}
}

// TestPrometheusEndpoint 验证 /metrics 输出 Prometheus 文本格式：
// 指标名/类型行齐全、含 build_info、且 TPS 无样本时不输出（避免 0 值误导告警）。
func TestPrometheusEndpoint(t *testing.T) {
	hub, _, _ := newAliasHub(t, nil)
	cfg := config.Default()
	cfg.MetricsFile = ""
	a := New(cfg, hub, log.New(io.Discard, "", 0))
	// 制造一条成功记录，让分组指标有内容。
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	a.serve(httptest.NewRecorder(), req, chatProtocol{})

	rec := httptest.NewRecorder()
	a.withAuth(a.apiPrometheus)(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type 应为 Prometheus 文本格式, got %q", ct)
	}
	out := rec.Body.String()
	for _, want := range []string{
		"# TYPE agent2api_requests_total counter",
		"agent2api_build_info{version=",
		"agent2api_in_flight_requests",
		`agent2api_model_requests_total{model="m"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("指标输出缺少 %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "agent2api_decode_tokens_per_second_avg") {
		t.Error("无 TPS 样本时不应输出平均解码速度（0 与没测过是两回事）")
	}
}
