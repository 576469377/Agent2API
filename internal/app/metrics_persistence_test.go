package app

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/obs"
)

func TestMetricsPersistenceStopsAndFinalSaveIncludesLaterCompletion(t *testing.T) {
	m := obs.New()
	path, storage := loadMetricsStorage(m, filepath.Join(t.TempDir(), "metrics.json"), log.New(io.Discard, "", 0))
	a := &App{metrics: m, metricsFile: path, metricsStorage: storage, logger: log.New(io.Discard, "", 0)}
	m.Record(obs.Record{Time: time.Now(), Model: "first", OK: true, InputTokens: 10})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := a.StartMetricsPersistence(ctx, time.Millisecond)
	if duplicate := a.StartMetricsPersistence(context.Background(), time.Nanosecond); duplicate != done {
		t.Fatal("repeated start created another persistence worker")
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for m.SavedAt().IsZero() {
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("periodic save did not run")
		}
	}
	cancel()
	select {
	case <-done:
	case <-deadline.C:
		t.Fatal("canceled persistence worker did not exit")
	}
	m.Record(obs.Record{Time: time.Now(), Model: "finished-during-shutdown", OK: true, InputTokens: 20})
	a.PersistMetricsNow()
	restored := obs.New()
	if err := restored.Load(path); err != nil {
		t.Fatal(err)
	}
	if got := restored.Snapshot(); got.Total != 2 || got.InputTokens != 30 {
		t.Fatalf("final completion was not persisted: total=%d tokens=%d", got.Total, got.InputTokens)
	}
}

func TestMetricsPersistenceCanceledBeforeStartDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	m := obs.New()
	_, storage := loadMetricsStorage(m, path, log.New(io.Discard, "", 0))
	a := &App{metrics: m, metricsFile: path, metricsStorage: storage, logger: log.New(io.Discard, "", 0)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	select {
	case <-a.StartMetricsPersistence(ctx, time.Nanosecond):
	default:
		t.Fatal("canceled worker did not terminate immediately")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("canceled worker wrote a snapshot: %v", err)
	}
}
