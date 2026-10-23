package trae

import (
	"testing"
	"time"
)

func TestCatalogInvalidationDoesNotClaimSavedAuthorizationIsMissing(t *testing.T) {
	a, _ := New(Config{})
	a.load = func(string) (Credential, error) {
		return Credential{ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}, nil
	}
	a.ready = true
	a.InvalidateModels()
	if a.Describe().Status != "degraded" {
		t.Fatal("saved authorization was presented as not signed in")
	}
	a.load = func(string) (Credential, error) { return Credential{}, loginRequired() }
	if a.Describe().Status != "unauthenticated" {
		t.Fatal("missing authorization was hidden")
	}
}
