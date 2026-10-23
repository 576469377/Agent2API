package zcode

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

func freeModel(platform, id string, now time.Time) Model {
	zero := 0.0
	source := map[string]string{"workbuddy": "upstream_config", "workbuddy-global": "upstream_config", "openrouter": "openrouter_models", "cursor": "cursor_free_plan"}[platform]
	return Model{Platform: platform, RoutingID: platform + "/" + id, DefaultRoute: true, ModelInfo: adapter.ModelInfo{ID: id, ContextTokens: 200000, MaxOutputTokens: 32000, SupportsThinking: true,
		Pricing: &adapter.ModelPricing{Status: "free", Source: source, CreditMultiplier: &zero, ObservedAt: now.Format(time.RFC3339)}}}
}

func allowanceModel(now time.Time) Model {
	m := freeModel("trae-cn", "model", now)
	rate := 0.2
	m.Pricing = &adapter.ModelPricing{Status: "credits", Source: "trae_account_catalog", CreditMultiplier: &rate, ObservedAt: now.Format(time.RFC3339)}
	m.Access = &adapter.ModelAccess{Basis: "recurring_free_allowance", Plan: "free", OverageEnabled: false, RemainingCredits: 100, Source: "trae_free_plan", CheckedAt: now.Format(time.RFC3339)}
	return m
}

func TestFreeSelectionRejectsUnverifiedPricesAndAllowances(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		change func(*Model)
	}{
		{"missing", func(m *Model) { m.Pricing = nil }},
		{"paid", func(m *Model) { m.Pricing.Status = "credits" }},
		{"dynamic", func(m *Model) { m.Pricing.Status = "dynamic" }},
		{"stale", func(m *Model) { m.Pricing.ObservedAt = now.Add(-6 * time.Minute).Format(time.RFC3339) }},
		{"future", func(m *Model) { m.Pricing.ObservedAt = now.Add(2 * time.Minute).Format(time.RFC3339) }},
		{"unknown source", func(m *Model) { m.Pricing.Source = "historical_snapshot" }},
		{"no limits", func(m *Model) { m.MaxOutputTokens = 0 }},
		{"route mismatch", func(m *Model) { m.RoutingID = "another/model" }},
		{"positive rate", func(m *Model) { value := 1.0; m.Pricing.CreditMultiplier = &value }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := freeModel("workbuddy", "free", now)
			tc.change(&m)
			if Eligible(m, now) {
				t.Fatal("unverified model accepted")
			}
		})
	}
	for _, platform := range []string{"workbuddy", "workbuddy-global", "openrouter", "cursor"} {
		if !Eligible(freeModel(platform, "model", now), now) {
			t.Fatal("verified free model rejected", platform)
		}
	}
	if !Eligible(allowanceModel(now), now) {
		t.Fatal("verified monthly free allowance rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*Model)
	}{
		{"paid plan", func(m *Model) { m.Access.Plan = "pro" }},
		{"overage", func(m *Model) { m.Access.OverageEnabled = true }},
		{"empty", func(m *Model) { m.Access.RemainingCredits = 0 }},
		{"infinite", func(m *Model) { m.Access.RemainingCredits = math.Inf(1) }},
		{"trial", func(m *Model) { m.Access.Basis = "trial" }},
		{"stale grant", func(m *Model) { m.Access.CheckedAt = now.Add(-6 * time.Minute).Format(time.RFC3339) }},
		{"no grant", func(m *Model) { m.Access = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := allowanceModel(now)
			tc.change(&m)
			if Eligible(m, now) {
				t.Fatal("unverified allowance accepted")
			}
		})
	}
}

func TestTraeSyncUpgradesLegacyWindowAndPreservesCustomBudgets(t *testing.T) {
	now := time.Now().UTC()
	for _, id := range []string{"deepseek-v4.1-flash", "glm-5.3-flash"} {
		for _, tc := range []struct {
			name                                     string
			context, output, wantContext, wantOutput int
		}{
			{"legacy", 200000, 32000, 1000000, 64000},
			{"smaller_output", 200000, 4096, 1000000, 4096},
			{"custom_context", 123456, 4096, 123456, 4096},
		} {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				model := allowanceModel(now)
				model.ID, model.RoutingID, model.ContextTokens, model.MaxOutputTokens = id, "trae-cn/"+id, 1000000, 64000
				rule := newRule(model)
				cfg := rule["config"].(object)
				cfg["enabled"], cfg["keep"] = false, "custom"
				cfg["properties"].(object)["contextWindow"] = tc.context
				cfg["optionSpecs"].(object)["maxOutputTokens"].(object)["max"] = tc.output
				source, _ := json.Marshal(object{"schemaVersion": 1, "config": object{"modelConfigRules": object{"manualProviderModelRules": []any{rule}}}})
				candidate, _, err := Prepare(source, []Model{model}, "http://localhost:8787", "", now)
				if err != nil {
					t.Fatal(err)
				}
				doc, _ := parse(candidate)
				rules := doc["config"].(object)["modelConfigRules"].(object)["manualProviderModelRules"].([]any)
				got := rules[0].(object)["config"].(object)
				if got["enabled"] != false || got["keep"] != "custom" || got["properties"].(object)["contextWindow"] != json.Number(fmt.Sprint(tc.wantContext)) || got["optionSpecs"].(object)["maxOutputTokens"].(object)["max"] != json.Number(fmt.Sprint(tc.wantOutput)) {
					t.Fatalf("unexpected TRAE migration: %+v", got)
				}
				again, result, err := Prepare(candidate, []Model{model}, "http://localhost:8787", "", now)
				if err != nil || result.Status != "unchanged" || string(again) != string(candidate) {
					t.Fatal("TRAE migration is not idempotent", err)
				}
			})
		}
	}
}

const existingConfig = `{"schemaVersion":1,"extension":{"large":9007199254740993},"config":{
"providerOrder":["other","agent2api"],
"providerConfigRules":{"extra":true,"providerRules":[{"providerId":"other","config":{"secret":"private-other-key"},"enabled":false},{"providerId":"agent2api","enabled":true,"custom":42,"config":{"personalModelIds":["shared","workbuddy/paid"],"modelOrder":["shared","workbuddy/paid"],"keep":"yes"}}]},
"modelConfigRules":{"providerModelRules":[],"manualProviderModelRules":[{"providerId":"other","modelId":"original","config":{"enabled":false}},{"providerId":"agent2api","modelId":"shared","config":{"enabled":false,"properties":{"contextWindow":777},"optionSpecs":{"reasoningLevel":{"values":["custom"]}}}},{"providerId":"agent2api","modelId":"workbuddy/paid","config":{"enabled":true}}]},
"defaultModelSelection":{"providerId":"other","modelId":"original","options":{"keep":true}}
}}`

func TestMergePreservesPreferencesOtherProvidersAndLargeNumbers(t *testing.T) {
	now := time.Now().UTC()
	models := []Model{freeModel("workbuddy", "shared", now), freeModel("workbuddy-global", "shared", now), freeModel("openrouter", "vendor/model:free", now), freeModel("cursor", "composer-2.5", now), allowanceModel(now)}
	models[1].DefaultRoute = false
	source := []byte(existingConfig)
	candidate, result, err := Prepare(source, models, "http://127.0.0.1:8799", "private-gateway-key", now)
	if err != nil {
		t.Fatal(err)
	}
	if result.ModelCount != 5 || len(result.Added) != 4 || len(result.Removed) != 1 || result.Retained != 1 || result.DefaultChanged {
		t.Fatalf("unexpected merge summary: %+v", result)
	}
	before, _ := parse(source)
	after, _ := parse(candidate)
	beforeCfg, afterCfg := before["config"].(object), after["config"].(object)
	if !reflect.DeepEqual(beforeCfg["defaultModelSelection"], afterCfg["defaultModelSelection"]) || !reflect.DeepEqual(before["extension"], after["extension"]) {
		t.Fatal("foreign default or precision changed")
	}
	oldProviders := beforeCfg["providerConfigRules"].(object)["providerRules"].([]any)
	providers := afterCfg["providerConfigRules"].(object)["providerRules"].([]any)
	if !reflect.DeepEqual(oldProviders[0], providers[0]) {
		t.Fatal("other provider changed")
	}
	provider := providers[1].(object)
	config := provider["config"].(object)
	if config["keep"] != "yes" || provider["custom"] != json.Number("42") || config["api"].(object)["baseUrl"] != "http://127.0.0.1:8799/v1" {
		t.Fatal("connection merge lost unrelated fields")
	}
	rules := afterCfg["modelConfigRules"].(object)["manualProviderModelRules"].([]any)
	byID := map[string]object{}
	for _, item := range rules {
		rule := item.(object)
		byID[rule["modelId"].(string)] = rule["config"].(object)
	}
	retained := byID["workbuddy/shared"]
	if retained["enabled"] != false || retained["properties"].(object)["contextWindow"] != json.Number("777") {
		t.Fatal("user overrides changed")
	}
	if byID["openrouter/vendor/model:free"]["optionSpecs"].(object)["maxOutputTokens"].(object)["max"] != json.Number("8192") {
		t.Fatal("OpenRouter output cap missing")
	}
	for _, id := range []string{"cursor/composer-2.5", "trae-cn/model"} {
		if byID[id]["optionSpecs"].(object)["reasoningLevel"].(object)["map"] != "{}" {
			t.Fatal("provider-controlled thinking changed")
		}
	}
	second, again, err := Prepare(candidate, models, "http://127.0.0.1:8799", "private-gateway-key", now)
	if err != nil || again.Status != "unchanged" || string(second) != string(candidate) {
		t.Fatalf("repeat sync was not a no-op: %v %+v", err, again)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("secret leaked in result")
	}
}

func TestRemovedDefaultIsRepairedButDisabledPreferencesRemain(t *testing.T) {
	now := time.Now().UTC()
	model := freeModel("workbuddy", "free", now)
	base := `{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"agent2api","enabled":%s,"config":{"personalModelIds":["workbuddy/paid","workbuddy/free"]}}]},"modelConfigRules":{"manualProviderModelRules":[{"providerId":"agent2api","modelId":"workbuddy/free","config":{"enabled":%s}}]},"defaultModelSelection":{"providerId":"agent2api","modelId":"workbuddy/paid"}}}`
	for _, tc := range []struct{ provider, model bool }{{true, true}, {true, false}, {false, true}} {
		source := []byte(strings.Replace(strings.Replace(base, "%s", map[bool]string{true: "true", false: "false"}[tc.provider], 1), "%s", map[bool]string{true: "true", false: "false"}[tc.model], 1))
		candidate, result, err := Prepare(source, []Model{model}, "http://localhost:8787", "", now)
		if err != nil {
			t.Fatal(err)
		}
		doc, _ := parse(candidate)
		selection, exists := doc["config"].(object)["defaultModelSelection"]
		if !result.DefaultChanged || exists != (tc.provider && tc.model) {
			t.Fatal("unsafe default repair", selection)
		}
		if result.ProviderEnabled != tc.provider {
			t.Fatal("provider preference changed")
		}
	}
}

func TestUnsupportedMalformedConflictingOrEmptyInputsDoNotProduceConfig(t *testing.T) {
	now := time.Now().UTC()
	m := freeModel("workbuddy", "shared", now)
	for _, source := range []string{`{"schemaVersion":2,"config":{}}`, `{"schemaVersion":1,"config":null}`, `{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":"bad"}}}`, existingConfig + `{}`} {
		if _, _, err := Prepare([]byte(source), []Model{m}, "http://localhost:8787", "", now); err == nil {
			t.Fatal("unsupported source accepted")
		}
	}
	doc, _ := parse([]byte(existingConfig))
	rules := doc["config"].(object)["modelConfigRules"].(object)
	rules["providerModelRules"] = []any{object{"providerId": ProviderID, "modelId": "workbuddy/shared", "config": object{}}}
	source, _ := json.Marshal(doc)
	if _, _, err := Prepare(source, []Model{m}, "http://localhost:8787", "", now); err == nil {
		t.Fatal("conflicting migration accepted")
	}
	if _, _, err := Prepare([]byte(existingConfig), nil, "http://localhost:8787", "", now); err == nil {
		t.Fatal("empty free catalogue would prune configuration")
	}
}

func TestStoreBacksUpOncePreservesHistoryAndRejectsInterveningEdits(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "provider_config.json")
	source := append([]byte{0xef, 0xbb, 0xbf}, []byte(existingConfig)...)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(root, "db.sqlite")
	os.WriteFile(history, []byte("history-sentinel"), 0o600)
	now := time.Now().UTC()
	candidate, _, err := Prepare(source, []Model{freeModel("workbuddy", "shared", now)}, "http://localhost:8787", "", now)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := Write(path, source, candidate)
	if err != nil {
		t.Fatal(err)
	}
	backedUp, _ := os.ReadFile(backup)
	if string(backedUp) != string(source) {
		t.Fatal("backup differs from original")
	}
	before, _ := os.Stat(path)
	again, err := Write(path, candidate, candidate)
	after, _ := os.Stat(path)
	if err != nil || again != "" || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("no-op rewrote configuration")
	}
	edited := []byte(`{"user":"new edit"}`)
	os.WriteFile(path, edited, 0o600)
	if _, err := Write(path, candidate, source); err == nil {
		t.Fatal("intervening edit overwritten")
	}
	current, _ := os.ReadFile(path)
	data, _ := os.ReadFile(history)
	if string(current) != string(edited) || string(data) != "history-sentinel" {
		t.Fatal("unrelated file changed")
	}
	temps, _ := filepath.Glob(filepath.Join(root, "*.tmp"))
	if len(temps) != 0 {
		t.Fatal("temporary files leaked")
	}
	lock := path + ".agent2api.lock"
	os.WriteFile(lock, []byte("other-writer"), 0o600)
	if _, err := Write(path, edited, source); err == nil {
		t.Fatal("another sync lock ignored")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("another writer's lock was removed")
	}
}
