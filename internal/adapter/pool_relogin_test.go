package adapter

import (
	"testing"
	"time"
)

func TestReauthenticationPreservesQuotaAndManualDisable(t *testing.T) {
	for _, state := range []accountState{stateBlocked, stateDisabled, stateReady} {
		p := NewPool("fixture", nil)
		deadline := time.Now().Add(time.Hour)
		account := &poolAccount{label: "fixture", state: state, reason: reasonUnauthorized, cooldownUntil: deadline, modelCooldown: map[string]time.Time{"model": deadline}}
		p.accounts = []*poolAccount{account}
		p.AccountReauthenticated("fixture")
		want := state
		if state == stateBlocked {
			want = stateReady
		}
		if account.state != want || account.modelCooldown["model"] != deadline {
			t.Fatal("relogin changed manual disable or model quota")
		}
		if state == stateBlocked && (!account.cooldownUntil.IsZero() || account.reason != "") {
			t.Fatal("authentication block not cleared")
		}
	}
}
