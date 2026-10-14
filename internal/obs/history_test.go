package obs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHistoryUsesUTCAndSeparatesPlatformModels(t *testing.T) {
	m := New()
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	local := day.Add(16*time.Hour + 10*time.Minute).In(time.FixedZone("UTC+8", 8*3600))
	for _, platform := range []string{"workbuddy", "workbuddy-global"} {
		m.Record(Record{Time: local, Platform: platform, Model: "hy3", OK: true, InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3, DurationMs: 7})
	}
	m.Record(Record{Time: day.Add(2 * time.Hour), Platform: "workbuddy", Model: "hy3", OK: false, InputTokens: 4, DurationMs: 9})
	h := m.Snapshot().UsageHistory
	if h.Timezone != "UTC" || len(h.Days) != 1 || h.Days[0].Date != day.Format(time.DateOnly) || len(h.Days[0].Models) != 2 {
		t.Fatalf("UTC and regional isolation: %+v", h)
	}
	domestic, global := h.Days[0].Models[0], h.Days[0].Models[1]
	if domestic.Total != 2 || domestic.Failed != 1 || domestic.OK != 1 || domestic.InputTokens != 14 || domestic.OutputTokens != 5 || domestic.DurationSum != 16 || global.Total != 1 {
		t.Fatalf("wrong daily totals: %+v", h.Days)
	}
	// Snapshot model rows must not point into mutable counters.
	h.Days[0].Models[0].InputTokens = 900
	if m.Snapshot().UsageHistory.Days[0].Models[0].InputTokens != 14 {
		t.Fatal("snapshot alias mutated metrics")
	}
}

func TestHistoryOutlivesRecentBufferAndSurvivesRestart(t *testing.T) {
	m := New()
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	for i := 0; i < 250; i++ {
		m.Record(Record{Time: yesterday, Platform: "workbuddy-global", Model: "hy3", OK: true, InputTokens: 2, OutputTokens: 1})
	}
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	restored := New()
	if err := restored.Load(path); err != nil {
		t.Fatal(err)
	}
	restored.Record(Record{Time: time.Now(), Platform: "workbuddy", Model: "hy3", OK: false})
	s := restored.Snapshot()
	if len(s.Recent) != recentCap || s.Total != 251 || len(s.UsageHistory.Days) != 2 {
		t.Fatalf("unexpected restoration: %+v", s.UsageHistory)
	}
	if got := s.UsageHistory.Days[0].Models[0]; got.Total != 250 || got.InputTokens != 500 || got.OutputTokens != 250 {
		t.Fatalf("daily history truncated: %+v", got)
	}
	if !s.UsageHistory.TrackingSince.Equal(m.trackingSince) {
		t.Fatal("restart changed coverage start")
	}
}

func TestLegacyHistoryBackfillIsPartialAndNotDuplicated(t *testing.T) {
	m := New()
	for i := 0; i < 250; i++ {
		m.Record(Record{Time: time.Now(), Platform: "workbuddy", Model: "hy3", OK: true, InputTokens: 1})
	}
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "usage_history")
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	restored := New()
	if err := restored.Load(path); err != nil {
		t.Fatal(err)
	}
	h := restored.Snapshot().UsageHistory
	if h.LegacyRequests != 250 || h.Days[0].Models[0].Total != 200 || h.TrackingSince.IsZero() {
		t.Fatalf("invented legacy history: %+v", h)
	}
	if err := restored.Save(path); err != nil {
		t.Fatal(err)
	}
	again := New()
	if err := again.Load(path); err != nil {
		t.Fatal(err)
	}
	again.Record(Record{Time: time.Now(), Platform: "workbuddy", Model: "hy3", OK: true})
	h = again.Snapshot().UsageHistory
	if h.Days[0].Models[0].Total != 201 || h.LegacyRequests != 250 || again.Snapshot().Total != 251 {
		t.Fatalf("legacy counted twice: %+v", h)
	}
}

func TestHistoryRetentionBoundsCalendarDates(t *testing.T) {
	m := New()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	m.Record(Record{Time: today.AddDate(0, 0, -366), Model: "expired", OK: true})
	m.Record(Record{Time: today.AddDate(0, 0, -365), Model: "oldest", OK: true})
	m.Record(Record{Time: today, Model: "current", OK: true})
	h := m.Snapshot().UsageHistory
	if h.RetentionDays != 366 || len(h.Days) != 2 || h.Days[0].Models[0].Model != "oldest" {
		t.Fatalf("retention: %+v", h)
	}
	if m.Snapshot().Total != 3 {
		t.Fatal("retention must not erase lifetime counters")
	}
}

func TestHistoryConcurrentRequestsDoNotLoseUsage(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				m.Record(Record{Time: time.Now(), Platform: "workbuddy", Model: "hy3", OK: true, InputTokens: 3})
			}
		}()
	}
	wg.Wait()
	var total, tokens int64
	for _, day := range m.Snapshot().UsageHistory.Days {
		for _, model := range day.Models {
			total += model.Total
			tokens += model.InputTokens
		}
	}
	if total != 256 || tokens != 768 {
		t.Fatalf("concurrent records lost usage: total=%d tokens=%d", total, tokens)
	}
}
