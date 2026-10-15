package app

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/obs"
)

func TestMetricsDefaultNameUpgradeKeepsAllDaysAndTotals(t *testing.T) {
	dir := t.TempDir()
	old, primary := filepath.Join(dir, "agent2api-metrics.json"), filepath.Join(dir, "metrics.json")
	history := obs.New()
	for day := -3; day <= -1; day++ {
		for i := 0; i < 250; i++ {
			history.Record(obs.Record{Time: time.Now().UTC().AddDate(0, 0, day), Model: "m", OK: true, InputTokens: 2})
		}
	}
	if err := history.Save(old); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(old)
	m := obs.New()
	path, storage := loadMetricsStorage(m, primary, log.New(io.Discard, "", 0))
	if path != primary || storage.state.LoadedFrom != old || m.Snapshot().Total != 750 || len(m.Snapshot().UsageHistory.Days) != 3 {
		t.Fatal("default filename change lost history")
	}
	a := &App{metrics: m, metricsFile: path, metricsStorage: storage, logger: log.New(io.Discard, "", 0)}
	m.Record(obs.Record{Model: "m", OK: true, Time: time.Now()})
	a.saveMetrics()
	again := obs.New()
	_, _ = loadMetricsStorage(again, primary, a.logger)
	if again.Snapshot().Total != 751 || len(again.Snapshot().UsageHistory.Days) != 4 {
		t.Fatal("upgrade restart lost days or doubled totals")
	}
	if after, _ := os.ReadFile(old); string(after) != string(original) {
		t.Fatal("archive changed")
	}
	custom := obs.New()
	_, _ = loadMetricsStorage(custom, filepath.Join(dir, "custom.json"), a.logger)
	if custom.Snapshot().Total != 0 {
		t.Fatal("explicit custom path must not guess another file")
	}
}

func TestExistingPrimaryImportsFullArchivedDaysWithoutSummingLifetime(t *testing.T) {
	dir := t.TempDir()
	old, primary := filepath.Join(dir, "agent2api-metrics.json"), filepath.Join(dir, "metrics.json")
	history, current := obs.New(), obs.New()
	for day := -4; day < 0; day++ {
		for i := 0; i < 210; i++ {
			history.Record(obs.Record{Time: time.Now().UTC().AddDate(0, 0, day), Model: "old", OK: true})
		}
	}
	current.Record(obs.Record{Time: time.Now(), Model: "today", OK: true})
	if err := history.Save(old); err != nil {
		t.Fatal(err)
	}
	if err := current.Save(primary); err != nil {
		t.Fatal(err)
	}
	m := obs.New()
	_, _ = loadMetricsStorage(m, primary, log.New(io.Discard, "", 0))
	s := m.Snapshot()
	if s.Total != 1 || len(s.UsageHistory.Days) != 5 {
		t.Fatalf("history must outlive recent200 without summing lifetime: %+v", s.UsageHistory)
	}
	for _, day := range s.UsageHistory.Days[:4] {
		if day.Models[0].Total != 210 {
			t.Fatal("aggregate truncated or counted twice")
		}
	}
	if err := m.Save(primary); err != nil {
		t.Fatal(err)
	}
	again := obs.New()
	_, _ = loadMetricsStorage(again, primary, log.New(io.Discard, "", 0))
	if again.Snapshot().UsageHistory.Days[0].Models[0].Total != 210 {
		t.Fatal("archive counted twice on restart")
	}
}

func TestCorruptHistoryRemainsIntactAndRecoverySurvivesRestart(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "metrics.json")
	broken := []byte(`{"total":999, "usage_history":`)
	if err := os.WriteFile(primary, broken, 0600); err != nil {
		t.Fatal(err)
	}
	m := obs.New()
	logger := log.New(io.Discard, "", 0)
	path, storage := loadMetricsStorage(m, primary, logger)
	if path != primary+".recovery" || storage.state.LoadError == "" || storage.state.Status != "recovery" {
		t.Fatal("corruption was hidden")
	}
	a := &App{metrics: m, metricsFile: path, metricsStorage: storage, logger: logger}
	m.Record(obs.Record{Time: time.Now(), Model: "m", OK: true})
	a.saveMetrics()
	if got, _ := os.ReadFile(primary); string(got) != string(broken) {
		t.Fatal("corrupt history overwritten")
	}
	if a.metricsStorageSnapshot().LastSavedAt == nil {
		t.Fatal("successful save not reported")
	}
	again := obs.New()
	_, _ = loadMetricsStorage(again, primary, logger)
	if again.Snapshot().Total != 1 {
		t.Fatal("recovery usage lost on restart")
	}
	a.metricsFile = filepath.Join(path, "not-a-directory")
	a.saveMetrics()
	if a.metricsStorageSnapshot().SaveError == "" {
		t.Fatal("save failure hidden")
	}
}

func TestDamagedRecoveryDoesNotLoseTheNextValidRecovery(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "metrics.json")
	for _, name := range []string{primary, primary + ".recovery"} {
		if err := os.WriteFile(name, []byte("broken"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := obs.New()
	logger := log.New(io.Discard, "", 0)
	path, _ := loadMetricsStorage(m, primary, logger)
	if path == primary || path == primary+".recovery" {
		t.Fatal("damaged file would be overwritten")
	}
	m.Record(obs.Record{Time: time.Now(), Model: "new", OK: true})
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	again := obs.New()
	reused, _ := loadMetricsStorage(again, primary, logger)
	if reused != path || again.Snapshot().Total != 1 {
		t.Fatal("second restart lost recovery data")
	}
}
