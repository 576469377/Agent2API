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
