package main

import (
	"io"
	"log"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter/kilo"
	"github.com/576469377/Agent2API/internal/config"
)

func TestKiloFactoryNeverBuildsWorkBuddyPoolOrInheritsCredentials(t *testing.T) {
	sandboxHome(t)
	cfg := config.Default()
	cfg.Upstream.BaseURL = "https://other.invalid"
	cfg.Upstream.CredentialPath = "must-not-read.session.json"
	cfg.Upstream.APIKey = "must-not-read"
	cfg.Upstream.APIKeyEnv = "OTHER_PROVIDER_KEY"
	cfg = cfg.CloneForPlatform(config.PlatformConfig{ID: kilo.Platform})
	if cfg.Upstream.BaseURL != "" || cfg.Upstream.CredentialPath != "" || cfg.Upstream.APIKey != "" || cfg.Upstream.APIKeyEnv != "" {
		t.Fatal("another platform's connection leaked into Kilo")
	}
	a, pool, err := buildAdapter(cfg, log.New(io.Discard, "", 0))
	if err != nil || pool != nil || a.Name() != kilo.Platform || len(platformDefaultPaths(kilo.Platform)) != 0 {
		t.Fatalf("anonymous source was treated as account pool: %T %+v %v", a, pool, err)
	}
}
