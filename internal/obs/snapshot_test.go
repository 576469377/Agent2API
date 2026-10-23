package obs

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSnapshotGroupOrderIsStableForEqualCounts(t *testing.T) {
	m := New()
	now := time.Now()
	for _, key := range []string{"z", "a", "m"} {
		m.Record(Record{Time: now, Model: key, Protocol: key, Account: key, OK: true})
	}
	for i := 0; i < 20; i++ {
		s := m.Snapshot()
		for _, groups := range [][]GroupStat{s.ByModel, s.ByProtocol, s.ByAccount} {
			for j, key := range []string{"a", "m", "z"} {
				if groups[j].Key != key {
					t.Fatalf("equal-count groups changed order: %+v", groups)
				}
			}
		}
	}
}

// Simulate a year of daily data without reading the user's persisted metrics.
func benchmarkHistory() *Metrics {
	m := New()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for day := 0; day < historyDays; day++ {
		models := make(map[usageKey]*UsageModel, 64)
		for model := 0; model < 64; model++ {
			key := usageKey{"platform", fmt.Sprintf("model-%02d", model)}
			models[key] = &UsageModel{Platform: key.platform, Model: key.model, Total: 10, InputTokens: 1000, OutputTokens: 500}
		}
		m.daily[today.AddDate(0, 0, -day).Format(time.DateOnly)] = models
	}
	return m
}

func BenchmarkSnapshotYearHistory(b *testing.B) {
	m := benchmarkHistory()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Snapshot()
	}
}

func BenchmarkRecordDuringHistorySnapshots(b *testing.B) {
	m := benchmarkHistory()
	stop := make(chan struct{})
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.Snapshot()
			}
		}
	}()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Record(Record{Time: time.Now(), Model: "active", Protocol: "chat", OK: true, InputTokens: 10})
	}
	b.StopTimer()
	close(stop)
	worker.Wait()
}
