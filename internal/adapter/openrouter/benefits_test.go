package openrouter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenRouterNullBudgetDoesNotBecomeZeroFreeAllowance(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/credits":
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"message":"private-echo"}}`)
		case "/key":
			fmt.Fprint(w, `{"data":{"limit":null,"limit_remaining":null,"usage":0,"usage_daily":0}}`)
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer s.Close()
	t.Setenv("BENEFITS_TEST_KEY", "")
	a := &Adapter{cfg: Config{BaseURL: s.URL, APIKey: "fixture", APIKeyEnv: "BENEFITS_TEST_KEY"}, client: s.Client()}
	if err := a.RefreshAccountBenefits(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := a.AccountBenefits()
	if len(v.Balances) != 1 || v.Balances[0].ID != "daily-spend" || v.Balances[0].Remaining != nil || v.Balances[0].Used == nil || *v.Balances[0].Used != 0 {
		t.Fatal(v)
	}
	a.cfg.APIKey = "new-account"
	if a.AccountBenefits().Status != "pending" {
		t.Fatal("old key balance reused")
	}
}
