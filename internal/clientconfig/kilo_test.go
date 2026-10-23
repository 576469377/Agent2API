package clientconfig

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestKiloWritersAndExpiredModelProtection(t *testing.T) {
	now := time.Now().UTC()
	m := free("kilo", "vendor/concrete:free", now)
	m.Pricing.Source = "kilo_models"
	m.Pricing.ExpiresAt = now.Add(time.Hour).Format(time.RFC3339Nano)
	if !Eligible(m, now) {
		t.Fatal("fresh free model rejected")
	}
	path, db := ccFixture(t)
	defer db.Close()
	snapshot, err := ReadCC(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := SyncCC(context.Background(), path, snapshot, []Model{m}, []string{"kilo"}, []string{"claude", "codex"}, "http://127.0.0.1:8787", "", now)
	if err != nil || result.ModelCount != 1 || result.ProviderCount != 2 {
		t.Fatalf("Kilo CC setup failed: %+v %v", result, err)
	}
	candidate, result, err := PreparePi(nil, []Model{m}, []string{"kilo"}, "http://127.0.0.1:8787", "", now)
	if err != nil || result.ModelCount != 1 {
		t.Fatal(err)
	}
	var pi map[string]any
	if json.Unmarshal(candidate, &pi) != nil || pi["providers"].(map[string]any)["agent2api-kilo"] == nil {
		t.Fatal("Kilo Pi provider missing")
	}
	m.Pricing.ExpiresAt = now.Format(time.RFC3339Nano)
	if Eligible(m, now) {
		t.Fatal("expired campaign remained eligible")
	}
	if _, _, err = PreparePi(candidate, []Model{m}, []string{"kilo"}, "http://127.0.0.1:8787", "", now); err == nil {
		t.Fatal("expired campaign rewrote Pi configuration")
	}
	for _, until := range []string{"invalid", "2000-01-01T00:00:00Z"} {
		m.Pricing.ExpiresAt = until
		if Eligible(m, now) {
			t.Fatal("invalid campaign accepted")
		}
	}
}
