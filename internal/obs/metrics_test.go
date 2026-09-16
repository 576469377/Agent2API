package obs

import (
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
