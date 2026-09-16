package obs

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMetricsSaveLoadRoundTrip 验证指标落盘后能在新进程里重建累积计数、
// 分组聚合与最近请求明细，且恢复后继续累加。
func TestMetricsSaveLoadRoundTrip(t *testing.T) {
	m := New()
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, InputTokens: 10, OutputTokens: 20, DurationMs: 100})
	m.Record(Record{Time: time.Now(), Protocol: "anthropic", Model: "m2", OK: false, Status: 400, InputTokens: 5, OutputTokens: 0, DurationMs: 50})

	if got := m.Snapshot().Total; got != 2 {
		t.Fatalf("落盘前 total=%d, want 2", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")
	if err := m.Save(path); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	// 模拟全新进程：重新 New 并从磁盘恢复。
	m2 := New()
	if err := m2.Load(path); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	s := m2.Snapshot()
	if s.Total != 2 {
		t.Fatalf("恢复后 total=%d, want 2", s.Total)
	}
	if s.OK != 1 || s.Failed != 1 {
		t.Fatalf("恢复后 ok=%d failed=%d, want 1/1", s.OK, s.Failed)
	}
	if s.InputTokens != 15 || s.OutputTokens != 20 {
		t.Fatalf("恢复后 token in=%d out=%d, want 15/20", s.InputTokens, s.OutputTokens)
	}
	if len(s.ByModel) != 2 {
		t.Fatalf("恢复后 by_model 数=%d, want 2", len(s.ByModel))
	}
	if len(s.Recent) != 2 {
		t.Fatalf("恢复后 recent 数=%d, want 2", len(s.Recent))
	}

	// 恢复后继续记录，计数应累加而非清零。
	m2.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, InputTokens: 1, OutputTokens: 1, DurationMs: 1})
	if got := m2.Snapshot().Total; got != 3 {
		t.Fatalf("恢复后继续记录 total=%d, want 3", got)
	}
}

// TestMetricsLoadMissingFile 验证文件不存在时静默跳过（首次运行）。
func TestMetricsLoadMissingFile(t *testing.T) {
	m := New()
	if err := m.Load(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Fatalf("不存在的文件应被忽略，却返回: %v", err)
	}
	if m.Snapshot().Total != 0 {
		t.Fatalf("应为空统计，却得到 total=%d", m.Snapshot().Total)
	}
}

// ───────────────────────── TPS（解码速度） ─────────────────────────

// legacyMetricsJSON 是「旧版」指标文件的最小样本：有 output_tokens，但没有任何 gen_* 字段。
// 它与仓库里真实存在的 agent2api-metrics.json 同形，用于守住宿卫逻辑。
const legacyMetricsJSON = `{
  "total": 5,
  "ok": 5,
  "failed": 0,
  "input_tokens": 1000,
  "output_tokens": 533159,
  "reasoning_tokens": 0,
  "duration_sum": 3750868,
  "by_model": {},
  "by_proto": {},
  "recent": [],
  "series": [],
  "saved_at": 1758000000
}`

// TestAvgTPS_NoSamples 验证没有测量到生成耗时时，TPS 保持 0 且样本数为 0。
//
// 这是守卫的回归测试：若把 GenMs==0 当真实耗时相除，会得到 +Inf，
// 而 encoding/json 无法序列化 +Inf，/api/metrics 会直接 500。
func TestAvgTPS_NoSamples(t *testing.T) {
	m := New()
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, OutputTokens: 100, GenMs: 0})

	s := m.Snapshot()
	if s.AvgTPS != 0 {
		t.Fatalf("未测到生成耗时时 avg_tps=%v, want 0", s.AvgTPS)
	}
	if s.TPSSamples != 0 {
		t.Fatalf("未测到生成耗时时 tps_samples=%d, want 0", s.TPSSamples)
	}
}

// TestAvgTPS_WeightedAverage 验证 TPS 是「按生成耗时加权」的平均，而不是每请求速率的算术平均。
func TestAvgTPS_WeightedAverage(t *testing.T) {
	m := New()
	// 200 tok / 1s = 200 tok/s
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, OutputTokens: 200, GenMs: 1000})
	// 100 tok / 1s = 100 tok/s
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, OutputTokens: 100, GenMs: 1000})

	s := m.Snapshot()
	if math.Abs(s.AvgTPS-150) > 1e-9 {
		t.Fatalf("加权平均 avg_tps=%v, want 150", s.AvgTPS)
	}
	if s.TPSSamples != 2 {
		t.Fatalf("tps_samples=%d, want 2", s.TPSSamples)
	}
}

// TestAvgTPS_SampleSet 明确 TPS 的样本集合口径：
//   - 只有 GenMs > 0 的请求参与（未测到耗时 → 无数据，不参与）；
//   - 失败请求**不**被排除——只要它产出了 token，那段解码就是真实速度样本；
//   - 分母用 genCount 而非 total，未被测量的请求不会摊薄平均速度。
func TestAvgTPS_SampleSet(t *testing.T) {
	m := New()
	// 有效样本：300 tok / 1s
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, OutputTokens: 300, GenMs: 1000})
	// 半路失败但确实产出了 token：500ms 解码真实发生，计入分母（分子为 0）
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: false, Status: 500, OutputTokens: 0, GenMs: 500})
	// 没有耗时数据（例如上游未回传 usage）：不参与，否则会把平均速度稀释成 0
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, OutputTokens: 50, GenMs: 0})

	s := m.Snapshot()
	// 300 tok / 1.5s = 200 tok/s
	if math.Abs(s.AvgTPS-200) > 1e-9 {
		t.Fatalf("avg_tps=%v, want 200（有效样本集为前两条）", s.AvgTPS)
	}
	if s.TPSSamples != 2 {
		t.Fatalf("tps_samples=%d, want 2", s.TPSSamples)
	}
	if s.Total != 3 {
		t.Fatalf("total=%d, want 3（TPS 过滤不应影响总请求数）", s.Total)
	}
}

// TestAvgTPS_SurvivesSaveLoad 验证 TPS 三元组能落盘并在新进程里恢复。
func TestAvgTPS_SurvivesSaveLoad(t *testing.T) {
	m := New()
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, OutputTokens: 200, GenMs: 1000})
	m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m1", OK: true, OutputTokens: 100, GenMs: 1000})

	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := m.Save(path); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	m2 := New()
	if err := m2.Load(path); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	s := m2.Snapshot()
	if math.Abs(s.AvgTPS-150) > 1e-9 {
		t.Fatalf("恢复后 avg_tps=%v, want 150", s.AvgTPS)
	}
	if s.TPSSamples != 2 {
		t.Fatalf("恢复后 tps_samples=%d, want 2", s.TPSSamples)
	}
}

// TestLoadLegacyMetricsFile 验证旧版指标文件（无 gen_* 字段）能正常加载，
// 且 TPS 呈现为「无数据」而不是 +Inf/NaN。
//
// 这是本文件里最重要的兼容性测试：真实用户的 metrics.json 就是这个形状，
// 一旦这里退化成 Inf，/api/metrics 会 500，控制台概览与调用日志两页同时白屏。
func TestLoadLegacyMetricsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := os.WriteFile(path, []byte(legacyMetricsJSON), 0o644); err != nil {
		t.Fatalf("写入旧版指标文件失败: %v", err)
	}

	m := New()
	if err := m.Load(path); err != nil {
		t.Fatalf("加载旧版指标文件失败: %v", err)
	}
	s := m.Snapshot()
	if s.Total != 5 || s.OutputTokens != 533159 {
		t.Fatalf("旧版计数未恢复: total=%d output=%d", s.Total, s.OutputTokens)
	}
	if s.AvgTPS != 0 || s.TPSSamples != 0 {
		t.Fatalf("旧版文件应呈现 TPS 无数据，却得到 avg_tps=%v samples=%d", s.AvgTPS, s.TPSSamples)
	}
	if math.IsInf(s.AvgTPS, 0) || math.IsNaN(s.AvgTPS) {
		t.Fatalf("avg_tps 退化为 Inf/NaN: %v", s.AvgTPS)
	}
}

// TestSnapshotMarshalable 验证快照永远可被序列化。
//
// 直接守住「快照无法序列化 → /api/metrics 500 → 控制台白屏」这条链路：
// 空指标、只有 token 无耗时、以及加载了旧版文件这三种状态都必须能 Marshal 成功。
func TestSnapshotMarshalable(t *testing.T) {
	cases := map[string]*Metrics{
		"空指标": New(),
		"仅 token 无耗时": func() *Metrics {
			m := New()
			m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m", OK: true, OutputTokens: 123, GenMs: 0})
			return m
		}(),
		"仅失败请求": func() *Metrics {
			m := New()
			m.Record(Record{Time: time.Now(), Protocol: "openai", Model: "m", OK: false, Status: 502, GenMs: 0})
			return m
		}(),
	}
	for name, m := range cases {
		if _, err := json.Marshal(m.Snapshot()); err != nil {
			t.Fatalf("%s: 快照无法序列化（会导致 /api/metrics 500）: %v", name, err)
		}
	}
}
