package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBenefitsRetainMeasuredZeroAndSeparateKeys(t *testing.T) {
	var cache BenefitCache
	if v := cache.Snapshot("a"); v.Status != "pending" || len(v.Balances) != 0 {
		t.Fatal(v)
	}
	err := cache.Refresh(context.Background(), "a", func(context.Context) (AccountBenefits, error) {
		return AccountBenefits{Balances: []AccountBalance{{ID: "credits", Unit: "credits", Remaining: BenefitAmount(0)}}}, nil
	})
	if err != nil || cache.Snapshot("a").Balances[0].Remaining == nil {
		t.Fatal("measured zero lost")
	}
	_ = cache.Refresh(context.Background(), "a", func(context.Context) (AccountBenefits, error) {
		return AccountBenefits{}, errors.New("secret-token-in-error")
	})
	v := cache.Snapshot("a")
	raw, _ := json.Marshal(v)
	if v.Status != "stale" || v.CheckedAt == nil || len(v.Balances) != 1 || strings.Contains(string(raw), "secret-token") {
		t.Fatal(string(raw))
	}
	if v = cache.Snapshot("b"); v.Status != "pending" || len(v.Balances) != 0 {
		t.Fatal("old account leaked", v)
	}
}
