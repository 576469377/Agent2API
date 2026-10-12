package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
)

func TestPricingSnapshotReplaceContainsOnlyCatalogueMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "_model-pricing.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	rate := .11
	snapshot := pricingSnapshot{Version: 1, Models: []pricingSnapshotModel{{Platform: "workbuddy", ID: "same", Pricing: &adapter.ModelPricing{Status: "credits", CreditMultiplier: &rate, Source: "upstream_config", ObservedAt: "2026-10-12T00:00:00Z"}}}}
	if err := writePricingSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip pricingSnapshot
	if err = json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Version != 1 || len(roundTrip.Models) != 1 || *roundTrip.Models[0].Pricing.CreditMultiplier != rate {
		t.Fatalf("invalid snapshot: %s", raw)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary export files leaked: %v", entries)
	}
}

func TestSnapshotExportIsReadOnlyAndPreservesPreviousFileOnFailure(t *testing.T) {
	sandboxHome(t)
	dir := t.TempDir()
	credential := filepath.Join(dir, "session.json")
	document := []byte(`{"auth":{"accessToken":"test-token","refreshToken":"test-refresh","domain":"www.workbuddy.ai"}}`)
	if err := os.WriteFile(credential, document, 0600); err != nil {
		t.Fatal(err)
	}
	var reject atomic.Bool
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v3/config" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if reject.Load() {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"models":[{"id":"same","credits":"x0.11"}]}}`))
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Upstream.Platforms = []config.PlatformConfig{{ID: "workbuddy-global", CredentialPath: credential, BaseURL: server.URL}}
	output := filepath.Join(dir, "_model-pricing.json")
	if err := exportPricingSnapshot(cfg, "", output); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(output)
	if calls.Load() != 1 || bytes.Contains(original, []byte("test-token")) || bytes.Contains(original, []byte("refreshToken")) {
		t.Fatalf("invalid export requests=%d", calls.Load())
	}
	reject.Store(true)
	if err := exportPricingSnapshot(cfg, "", output); err == nil {
		t.Fatal("expected rejected configuration")
	}
	retained, _ := os.ReadFile(output)
	untouched, _ := os.ReadFile(credential)
	if calls.Load() != 2 || !bytes.Equal(original, retained) || !bytes.Equal(document, untouched) {
		t.Fatal("read-only export changed credentials or previous metadata")
	}
}
