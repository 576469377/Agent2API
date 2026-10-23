package cursor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCursorUsageKeepsUnknownRemainderAndConvertsCents(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/aiserver.v1.DashboardService/GetCurrentPeriodUsage" {
			t.Error("unexpected operation", r.URL.Path)
		}
		fmt.Fprint(w, `{"billingCycleEnd":"1800000000000","planUsage":{"totalSpend":103}}`)
	}))
	defer s.Close()
	a, err := New(Config{BaseURL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	a.load = func(context.Context, string) (credentials, error) {
		return credentials{token: "fixture", version: "3.24.18"}, nil
	}
	if err = a.RefreshAccountBenefits(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := a.AccountBenefits().Balances[0]
	if b.Used == nil || *b.Used != 1.03 || b.Remaining != nil || b.Total != nil || b.ResetsAt == nil {
		t.Fatal(b)
	}
}
