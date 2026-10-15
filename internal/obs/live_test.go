package obs

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLiveCountsEstimatesAndCleanup(t *testing.T) {
	m := New()
	now := time.Now()
	id := m.BeginLive("chat", now.Add(-10*time.Second))
	m.RouteLive(id, "global", "m")
	m.ObserveLive(id, 8, now)
	initial := m.liveSnapshotAt(now)
	if initial.InFlight != 1 || initial.Active[0].OutputCharacters != 8 || initial.Active[0].EstimatedOutputTokens != nil {
		t.Fatalf("uncalibrated progress: %+v", initial)
	}
	m.Record(Record{LiveID: id, Time: now, Platform: "global", Model: "m", OK: true, OutputCharacters: 8, OutputTokens: 4, GenMs: 1000})
	if got := m.LiveSnapshot(); got.InFlight != 0 || len(got.Active) != 0 || got.LastCompleted.OutputTokens != 4 {
		t.Fatalf("completion leaked: %+v", got)
	}
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	restored := New()
	if err := restored.Load(path); err != nil {
		t.Fatal(err)
	}
	id = restored.BeginLive("chat", now.Add(-2*time.Second))
	restored.RouteLive(id, "global", "m")
	restored.ObserveLive(id, 4, now)
	live := restored.liveSnapshotAt(now).Active[0]
	if live.EstimatedOutputTokens == nil || *live.EstimatedOutputTokens != 2 || live.EstimatedTokensPerSecond == nil || *live.EstimatedTokensPerSecond != 1 {
		t.Fatalf("calibration lost: %+v", live)
	}
	if got := restored.liveSnapshotAt(now.Add(5 * time.Second)).Active[0].CharactersPerSecond; got != 0 {
		t.Fatalf("stalled stream still generating: %v", got)
	}
	other := restored.BeginLive("chat", now)
	restored.RouteLive(other, "domestic", "m")
	restored.ObserveLive(other, 4, now)
	if got := restored.liveSnapshotAt(now).Active[1]; got.EstimatedOutputTokens != nil {
		t.Fatal("calibration crossed platforms")
	}
	restored.Record(Record{LiveID: id, Time: now, OK: false})
	restored.Record(Record{LiveID: other, Time: now, OK: false})
	if got := restored.LiveSnapshot(); len(got.Active) != 0 || got.InFlight != 0 {
		t.Fatal("failed streams leaked")
	}
}
