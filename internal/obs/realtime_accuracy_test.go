package obs

import (
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRPMCountsArrivalsAndExpiresWithoutCompletions(t *testing.T) {
	m := New()
	now := time.Now()
	first := m.BeginLive("chat", now.Add(-59*time.Second))
	second := m.BeginLive("chat", now)
	if got := m.lastMinuteRPMLocked(now); got != 2 {
		t.Fatalf("pending arrivals absent from RPM: %d", got)
	}
	if got := m.lastMinuteRPMLocked(now.Add(2 * time.Second)); got != 1 {
		t.Fatalf("expired arrival did not leave window: %d", got)
	}
	if got := m.lastMinuteRPMLocked(now.Add(61 * time.Second)); got != 0 {
		t.Fatalf("idle RPM stuck at %d", got)
	}
	m.Record(Record{LiveID: first, Time: now.Add(-59 * time.Second), OK: true})
	m.Record(Record{LiveID: second, Time: now, OK: true})
	if got := m.lastMinuteRPMLocked(now); got != 2 {
		t.Fatalf("completion changed arrival count: %d", got)
	}
}

func TestConcurrentCompletionOrderDoesNotOverwriteMinuteCounts(t *testing.T) {
	m := New()
	now := time.Now().Truncate(time.Minute)
	for _, minute := range []time.Time{now.Add(-time.Minute), now, now.Add(-time.Minute)} {
		m.Record(Record{Time: minute, OK: true, InputTokens: 10, OutputTokens: 5, GenMs: 1000})
	}
	s := m.Snapshot()
	var total, input, output int64
	for _, b := range s.Series {
		total += b.Total
		input += b.InputTokens
		output += b.OutputTokens
	}
	if total != 3 || input != 30 || output != 15 {
		t.Fatalf("out-of-order completions lost: %d/%d/%d", total, input, output)
	}
	restored := normalizeMinuteBuckets([]minuteBucket{{Minute: 1, Total: 1, InputTokens: 10}, {Minute: 2, Total: 3}, {Minute: 1, Total: 2, InputTokens: 20}})
	if len(restored) != 2 || restored[0].Total != 3 || restored[0].InputTokens != 30 || restored[1].Total != 3 {
		t.Fatalf("legacy duplicate buckets overwritten: %+v", restored)
	}
}

func TestMetricsSnapshotsStayConsistentDuringConcurrentUpdates(t *testing.T) {
	m := New()
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 250; i++ {
				started := time.Now()
				id := m.BeginLive("chat", started)
				m.Record(Record{LiveID: id, Time: started, Protocol: "chat", Platform: "p", Model: "m", OK: i%2 == 0, InputTokens: 2, OutputTokens: 3})
			}
		}()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	for {
		s := m.Snapshot()
		var groups int64
		for _, g := range s.ByModel {
			groups += g.Total
		}
		if s.Total != s.OK+s.Failed || s.Total != groups || s.InputTokens != 2*s.Total || s.OutputTokens != 3*s.Total {
			t.Fatalf("mixed snapshot: %+v", s)
		}
		live := m.LiveSnapshot()
		if live.InFlight != int64(len(live.Active)) {
			t.Fatalf("in-flight and active disagree: %+v", live)
		}
		select {
		case <-done:
			goto complete
		default:
		}
	}
complete:
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	restored := New()
	if err := restored.Load(path); err != nil {
		t.Fatal(err)
	}
	if s := restored.Snapshot(); s.Total != 1000 || s.OK != 500 || s.Failed != 500 || s.InFlight != 0 {
		t.Fatalf("save/load mismatch: %+v", s)
	}
}

func TestLiveRateUsesAnActualThreeSecondWindowAndCompletionTime(t *testing.T) {
	m := New()
	start := time.Unix(1000, 0)
	id := m.BeginLive("chat", start)
	m.ObserveLive(id, 12, start.Add(900*time.Millisecond))
	if rate := m.liveSnapshotAt(start.Add(3100 * time.Millisecond)).Active[0].CharactersPerSecond; math.Abs(rate-4) > 1e-9 {
		t.Fatalf("second rounding discarded a valid sample: %v", rate)
	}
	if rate := m.liveSnapshotAt(start.Add(4 * time.Second)).Active[0].CharactersPerSecond; rate != 0 {
		t.Fatalf("stalled stream reports speed %v", rate)
	}
	m.Record(Record{LiveID: id, Time: start, DurationMs: 5000, OK: true, OutputTokens: 10, GenMs: 1000})
	m.Record(Record{Time: start.Add(10 * time.Second), OK: false, OutputTokens: 20, GenMs: 1000})
	latest := m.LiveSnapshot().LastCompleted
	if latest == nil || !latest.Time.Equal(start.Add(5*time.Second)) || latest.OutputTokens != 10 {
		t.Fatalf("last measured success has wrong time/state: %+v", latest)
	}
}
