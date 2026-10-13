package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImageLimitDefaultsAndOverrides(t *testing.T) {
	cfg := Default()
	if cfg.Models.ImageLimitFor("workbuddy-global", "hy3").MaxImages != 200 || cfg.Models.ImageLimitFor("workbuddy-global", "step-5-preview").MaxImages != 60 {
		t.Fatal("missing empirical defaults")
	}
	for _, platform := range []string{"workbuddy", "another-platform"} {
		if cfg.Models.ImageLimitFor(platform, "hy3") != nil {
			t.Fatal("unmeasured regions must not inherit Global limits")
		}
	}
	if cfg.Models.ImageLimitFor("workbuddy-global", "deepseek-v4.1-flash") != nil {
		t.Fatal("no measured limit is not a known unlimited capacity")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"models":{"image_limits":{"workbuddy-global/hy3":7,"workbuddy-global/step-5-preview":0,"workbuddy/hy3":8}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Models.ImageLimitFor("workbuddy-global", "hy3").MaxImages != 7 || loaded.Models.ImageLimitFor("workbuddy-global", "step-5-preview").Enabled || loaded.Models.ImageLimitFor("workbuddy", "hy3").MaxImages != 8 {
		t.Fatal("explicit overrides were not respected")
	}
}

func TestInvalidImageLimitConfigurationIsRejected(t *testing.T) {
	for _, text := range []string{`{"hy3":60}`, `{"workbuddy-global/":60}`, `{"/hy3":60}`, `{" workbuddy-global/hy3":60}`, `{"workbuddy-global/hy3":-1}`, `{"workbuddy-global/hy3":1.5}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"models":{"image_limits":`+text+`}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("invalid override accepted: %s", text)
		}
	}
}

func TestImageCleanupRequiresExplicitPerModelOptIn(t *testing.T) {
	cfg := Default()
	if cfg.Models.ImageLimitFor("workbuddy-global", "hy3").OverflowPolicy != ImageOverflowReject {
		t.Fatal("image removal must never become a silent default")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"models":{"image_overflow_policies":{"workbuddy-global/hy3":"trim_oldest","workbuddy-global/step-5-preview":"reject"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Models.ImageLimitFor("workbuddy-global", "hy3").OverflowPolicy != ImageOverflowTrimOldest || loaded.Models.ImageLimitFor("workbuddy-global", "step-5-preview").OverflowPolicy != ImageOverflowReject || loaded.Models.ImageOverflowPolicyFor("workbuddy", "hy3") != ImageOverflowReject {
		t.Fatal("opt-in crossed models or regions")
	}
	for _, raw := range []string{`{"hy3":"trim_oldest"}`, `{"workbuddy-global/hy3":"automatic"}`, `{"workbuddy-global/hy3":""}`} {
		if err := os.WriteFile(path, []byte(`{"models":{"image_overflow_policies":`+raw+`}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("invalid image policy was accepted")
		}
	}
}
