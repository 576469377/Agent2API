package app

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/obs"
)

type MetricsStorage struct {
	Enabled     bool       `json:"enabled"`
	Status      string     `json:"status"`
	File        string     `json:"file,omitempty"`
	LoadedFrom  string     `json:"loaded_from,omitempty"`
	LoadError   string     `json:"load_error,omitempty"`
	SaveError   string     `json:"save_error,omitempty"`
	LastSavedAt *time.Time `json:"last_saved_at,omitempty"`
}
type metricsStorage struct {
	sync.RWMutex
	state     MetricsStorage
	startOnce sync.Once
	done      chan struct{}
}

// Only the two documented default names in the same directory are eligible.
// Explicit custom paths are never replaced by a guessed file elsewhere.
func alternateMetricsFile(path string) string {
	name := filepath.Base(path)
	switch name {
	case "metrics.json":
		name = "agent2api-metrics.json"
	case "agent2api-metrics.json":
		name = "metrics.json"
	default:
		return ""
	}
	other := filepath.Join(filepath.Dir(path), name)
	if stat, err := os.Stat(other); err == nil && stat.Mode().IsRegular() {
		return other
	}
	return ""
}

func loadMetricsStorage(metrics *obs.Metrics, path string, logger *log.Logger) (string, *metricsStorage) {
	store := &metricsStorage{state: MetricsStorage{Status: "disabled"}}
	if path == "" {
		return "", store
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	store.state = MetricsStorage{Enabled: true, Status: "ready", File: path}
	loadFrom := path
	_, statErr := os.Stat(path)
	if os.IsNotExist(statErr) {
		store.state.Status = "new"
		if alternate := alternateMetricsFile(path); alternate != "" {
			loadFrom = alternate
		}
	}
	if err := metrics.Load(loadFrom); err != nil {
		// Preserve the original bytes. Continue in a separate recovery file so
		// neither the damaged history nor newly recorded usage is overwritten.
		store.state.LoadError = err.Error()
		store.state.Status = "recovery"
		path = loadMetricsRecovery(metrics, path+".recovery")
		store.state.File = path
		if _, err := os.Stat(path); err == nil {
			store.state.LoadedFrom = path
		}
		logger.Printf("历史指标读取失败，原文件保留，新指标写入独立恢复文件: %v", err)
	} else {
		if _, err := os.Stat(loadFrom); err == nil {
			store.state.LoadedFrom = loadFrom
			store.state.Status = "ready"
		}
		if loadFrom == path {
			if alternate := alternateMetricsFile(path); alternate != "" {
				if err := metrics.ImportHistory(alternate, nil); err != nil {
					logger.Printf("旧指标存档读取失败: %v", err)
				}
			}
		}
	}
	return path, store
}

func loadMetricsRecovery(metrics *obs.Metrics, path string) string {
	_, statErr := os.Stat(path)
	if statErr == nil {
		if err := metrics.Load(path); err == nil {
			return path
		}
	}
	// If a previous recovery was also damaged, reuse the newest valid recovery
	// created by this gateway, rather than starting another empty file each boot.
	entries, _ := os.ReadDir(filepath.Dir(path))
	var candidates []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), filepath.Base(path)+"-") {
			candidates = append(candidates, filepath.Join(filepath.Dir(path), entry.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
	for _, candidate := range candidates {
		if err := metrics.Load(candidate); err == nil {
			return candidate
		}
	}
	if os.IsNotExist(statErr) {
		return path
	}
	return path + "-" + time.Now().UTC().Format("20060102T150405.000000000")
}

// StartMetricsPersistence owns one worker for the application's lifetime.
// Cancellation stops future passes; shutdown can still call PersistMetricsNow
// after request accounting completes. The result closes when the worker exits.
func (a *App) StartMetricsPersistence(ctx context.Context, interval time.Duration) <-chan struct{} {
	a.metricsStorage.startOnce.Do(func() {
		a.metricsStorage.done = make(chan struct{})
		if a.metricsFile == "" || ctx.Err() != nil {
			close(a.metricsStorage.done)
			return
		}
		if interval <= 0 {
			interval = 30 * time.Second
		}
		go func() {
			defer close(a.metricsStorage.done)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if ctx.Err() != nil {
						return
					}
					a.saveMetrics()
				}
			}
		}()
	})
	return a.metricsStorage.done
}

func (a *App) metricsStorageSnapshot() MetricsStorage {
	a.metricsStorage.RLock()
	out := a.metricsStorage.state
	a.metricsStorage.RUnlock()
	if stamp := a.metrics.SavedAt(); !stamp.IsZero() {
		out.LastSavedAt = &stamp
	}
	return out
}

func (a *App) saveMetrics() {
	if a.metricsFile == "" {
		return
	}
	a.metricsStorage.Lock()
	defer a.metricsStorage.Unlock()
	err := a.metrics.Save(a.metricsFile)
	if err != nil {
		a.metricsStorage.state.SaveError = err.Error()
		a.logger.Printf("指标落盘失败: %v", err)
	} else {
		a.metricsStorage.state.SaveError = ""
		if a.metricsStorage.state.Status == "new" {
			a.metricsStorage.state.Status = "ready"
		}
	}
}
