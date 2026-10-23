package kiro

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExhaustedFreeBalanceIsMeasuredZeroWithoutAllowingPrompts(t *testing.T) {
	a := mockAdapter(t)
	ctx := context.Background()
	if err := a.RefreshAccountBenefits(ctx); err != nil {
		t.Fatal(err)
	}
	if v := a.AccountBenefits(); len(v.Balances) != 1 || v.Balances[0].Remaining == nil || *v.Balances[0].Remaining <= 0 {
		t.Fatal("initial measured allowance missing", v)
	}
	t.Setenv("AGENT2API_KIRO_EXHAUSTED", "1")
	audit := filepath.Join(t.TempDir(), "prompts")
	t.Setenv("AGENT2API_KIRO_AUDIT", audit)
	if err := a.RefreshAccountBenefits(ctx); err != nil {
		t.Fatal("zero is a valid balance reading", err)
	}
	v := a.AccountBenefits()
	if v.Status != "ready" || len(v.Balances) != 1 || v.Balances[0].Remaining == nil || *v.Balances[0].Remaining != 0 || *v.Balances[0].Used != 50 {
		t.Fatal("exhausted balance retained an old positive allowance", v)
	}
	if _, err := a.Stream(ctx, request()); !freeCreditsExhausted(err) {
		t.Fatal("exhausted account was allowed to prompt", err)
	}
	if _, err := os.Stat(audit); !os.IsNotExist(err) {
		t.Fatal("generation reached the CLI", err)
	}
}
