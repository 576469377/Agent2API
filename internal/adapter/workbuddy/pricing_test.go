package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCatalogueCreditRates(t *testing.T) {
	for _, tc := range []struct {
		raw, desc, status string
		rate              *float64
	}{
		{`"x0.00"`, "", "free", ratePointer(0)},
		{`"x0.11"`, "", "credits", ratePointer(.11)},
		{`"x0.34 credits"`, "", "credits", ratePointer(.34)},
		{`" x0.001 "`, "", "credits", ratePointer(.001)},
		{`null`, "", "unknown", nil}, {`0`, "", "unknown", nil},
		{`""`, "", "unknown", nil}, {`"free"`, "", "unknown", nil},
		{`"x-1"`, "", "unknown", nil}, {`"x1.2 dollars"`, "", "unknown", nil},
		{`""`, "自动匹配模型，积分倍率随之浮动", "dynamic", nil},
		{`"x0.05"`, "积分倍率随之浮动", "credits", ratePointer(.05)},
	} {
		p := modelPricing(configModel{Credits: json.RawMessage(tc.raw), DescriptionZh: tc.desc}, "2026-10-12T00:00:00Z")
		if p.Status != tc.status || p.Source != "upstream_config" || p.ObservedAt == "" {
			t.Fatalf("raw=%s pricing=%+v", tc.raw, p)
		}
		if tc.rate == nil {
			if p.CreditMultiplier != nil {
				t.Fatalf("must not invent zero: %+v", p)
			}
		} else if p.CreditMultiplier == nil || *p.CreditMultiplier != *tc.rate {
			t.Fatalf("raw=%s rate=%v", tc.raw, p.CreditMultiplier)
		}
	}
}
func ratePointer(value float64) *float64 { return &value }

func TestReadOnlyCatalogueDoesNotGenerateOrRefresh(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != configPath {
			t.Errorf("unexpected operation %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"models":[{"id":"same","credits":"x0.00"},{"id":"unknown"}]}}`))
	}))
	defer srv.Close()
	a, err := New(Config{Platform: GlobalPlatform, BaseURL: srv.URL, CredentialPath: globalTestCredential(t, "www.workbuddy.ai")})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ReadModelCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || len(models) != 2 || models[0].Pricing.Status != "free" || models[1].Pricing.Status != "unknown" {
		t.Fatalf("requests=%d models=%+v", requests.Load(), models)
	}
	// A rejected access token must be surfaced; this read-only path must not refresh.
	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Error("refresh attempted")
		}
		w.WriteHeader(401)
	}))
	defer rejected.Close()
	a.cli.baseURL = rejected.URL
	if _, err = a.ReadModelCatalog(context.Background()); err == nil || requests.Load() != 2 {
		t.Fatalf("rejected read requests=%d err=%v", requests.Load(), err)
	}
}
