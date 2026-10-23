package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/openrouter"
	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/config"
)

// This allowlist contains no account identity, credentials, or raw config data.
type pricingSnapshotModel struct {
	Platform string                `json:"platform"`
	ID       string                `json:"id"`
	Pricing  *adapter.ModelPricing `json:"pricing"`
}
type pricingSnapshot struct {
	Version int                    `json:"version"`
	Models  []pricingSnapshotModel `json:"models"`
}

func exportPricingSnapshot(cfg config.Config, platform, output string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snapshot := pricingSnapshot{Version: 1, Models: []pricingSnapshotModel{}}
	selected := 0
	for _, pc := range cfg.ResolvePlatforms() {
		if platform != "" && platform != pc.ID {
			continue
		}
		selected++
		var models []adapter.ModelInfo
		if pc.ID == openrouter.Platform || pc.ID == "cursor" || pc.ID == "trae-cn" || pc.ID == "kiro" {
			a, _, err := buildAdapter(cfg.CloneForPlatform(pc), quietPricingLogger{})
			if err != nil {
				return err
			}
			models, err = a.ListModels(ctx)
			if err != nil {
				return err
			}
		} else {
			for _, path := range collectCredentialPaths(cfg.CloneForPlatform(pc)) {
				a, err := workbuddy.New(workbuddy.Config{Platform: pc.ID, BaseURL: cfg.CloneForPlatform(pc).Upstream.BaseURL, CredentialPath: path})
				if err != nil {
					continue
				}
				models, err = a.ReadModelCatalog(ctx)
				if err == nil && len(models) > 0 {
					break
				}
				models = nil
			}
		}
		if len(models) == 0 {
			return fmt.Errorf("平台 %s 未取得模型配置，保留原快照；请检查登录状态", pc.ID)
		}
		for _, m := range models {
			snapshot.Models = append(snapshot.Models, pricingSnapshotModel{Platform: pc.ID, ID: m.ID, Pricing: m.Pricing})
		}
	}
	if selected == 0 {
		return fmt.Errorf("没有匹配的平台")
	}
	return writePricingSnapshot(output, snapshot)
}

type quietPricingLogger struct{}

func (quietPricingLogger) Printf(string, ...any) {}

func writePricingSnapshot(output string, snapshot pricingSnapshot) error {
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	// Atomic replacement avoids a browser reading a partially written JSON file.
	file, err := os.CreateTemp(filepath.Dir(output), ".model-pricing-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, output)
}
