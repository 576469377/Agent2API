package zcode

import (
	"encoding/json"
	"testing"
	"time"
)

func TestKiroUnknownOutputNeedsVerifiedAllowanceAndUsesOnlyClientBudget(t *testing.T) {
	now := time.Now().UTC()
	m := allowanceModel(now)
	m.Platform = "kiro"
	m.RoutingID = "kiro/model"
	m.MaxOutputTokens = 0
	m.SupportsThinking = false
	m.Pricing.Source = "kiro_cli_catalog"
	m.Access.Source = "kiro_cli_free_plan"
	if !Eligible(m, now) || outputLimit(m) != 8192 {
		t.Fatal("verified Kiro model rejected")
	}
	for _, change := range []func(){func() { m.Access.Plan = "pro" }, func() { m.Access.Source = "trae_free_plan" }, func() { m.Access.RemainingCredits = 0 }, func() { m.Pricing.Source = "trae_account_catalog" }} {
		savedP, savedA := *m.Pricing, *m.Access
		change()
		if Eligible(m, now) {
			t.Fatal("unverified Kiro billing accepted")
		}
		*m.Pricing, *m.Access = savedP, savedA
	}
	rule := newRule(m)
	cfg := rule["config"].(object)
	spec := cfg["optionSpecs"].(object)
	if spec["reasoningLevel"].(object)["map"] != "{}" || spec["maxOutputTokens"].(object)["max"] != 8192 {
		t.Fatal("unsupported Kiro parameters injected")
	}
	spec["maxOutputTokens"].(object)["max"] = json.Number("1024")
	if e := adjustOptions(cfg, m); e != nil || spec["maxOutputTokens"].(object)["max"] != json.Number("1024") {
		t.Fatal("custom smaller output budget overwritten", e)
	}
	if m.MaxOutputTokens != 0 {
		t.Fatal("published model metadata invented")
	}
}
