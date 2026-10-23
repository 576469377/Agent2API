package zcode

import (
	"strings"
	"testing"
	"time"
)

func TestKiloZCodeWriteUsesVerifiedCapacityAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	m := freeModel("kilo", "vendor/concrete:free", now)
	m.Pricing.Source = "kilo_models"
	m.Pricing.ExpiresAt = now.Add(time.Hour).Format(time.RFC3339Nano)
	m.ContextTokens, m.MaxOutputTokens = 1000000, 64000
	candidate, result, err := Prepare([]byte(existingConfig), []Model{m}, "http://127.0.0.1:8787", "", now)
	if err != nil || !strings.Contains(string(candidate), "kilo/vendor/concrete:free") || result.ModelCount != 1 || !strings.Contains(string(candidate), "1000000") {
		t.Fatalf("Kilo ZCode setup failed: %+v %v", result, err)
	}
	m.Pricing.ExpiresAt = now.Format(time.RFC3339Nano)
	if _, _, err := Prepare(candidate, []Model{m}, "http://127.0.0.1:8787", "", now); err == nil {
		t.Fatal("expired-only catalogue rewrote configuration")
	}
}
