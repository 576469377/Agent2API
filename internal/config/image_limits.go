package config

import (
	"fmt"
	"strings"
)

// TODO: Recheck these empirical limits when upstream behavior changes.
// Source: user-supplied real-request experiments, 2026-10-13/14; counts include
// historical tool-result images. No limit is inferred for China or other models.
var builtinImageLimits = map[string]int{
	"workbuddy-global/hy3":            200,
	"workbuddy-global/step-5-preview": 60,
}

const (
	ImageOverflowReject     = "reject"
	ImageOverflowTrimOldest = "trim_oldest"
)

// ImageLimit describes the effective gateway guard, not a promise about upstream capacity.
type ImageLimit struct {
	MaxImages      int    `json:"max_images"`
	Enabled        bool   `json:"enabled"`
	Source         string `json:"source"`
	ObservedOn     string `json:"observed_on,omitempty"`
	OverflowPolicy string `json:"overflow_policy"`
}

func (m ModelsConfig) ImageOverflowPolicyFor(platform, model string) string {
	if m.ImageOverflowPolicies[platform+"/"+model] == ImageOverflowTrimOldest {
		return ImageOverflowTrimOldest
	}
	return ImageOverflowReject
}

func (m ModelsConfig) ImageLimitFor(platform, model string) *ImageLimit {
	key := platform + "/" + model
	policy := m.ImageOverflowPolicyFor(platform, model)
	if count, ok := m.ImageLimits[key]; ok {
		return &ImageLimit{MaxImages: count, Enabled: count > 0, Source: "config", OverflowPolicy: policy}
	}
	if count, ok := builtinImageLimits[key]; ok {
		return &ImageLimit{MaxImages: count, Enabled: true, Source: "builtin_empirical", ObservedOn: "2026-10-14", OverflowPolicy: policy}
	}
	return nil // Unknown is not unlimited.
}

func (m ModelsConfig) validateImageLimits() error {
	for key, count := range m.ImageLimits {
		platform, model, ok := strings.Cut(key, "/")
		if !ok || platform == "" || model == "" || strings.TrimSpace(key) != key || strings.TrimSpace(platform) != platform || strings.TrimSpace(model) != model {
			return fmt.Errorf("models.image_limits 的键必须为平台/模型名: %q", key)
		}
		if count < 0 {
			return fmt.Errorf("models.image_limits[%q] 必须为非负整数；0 表示关闭该模型的图片数量护栏", key)
		}
	}
	for key, policy := range m.ImageOverflowPolicies {
		platform, model, ok := strings.Cut(key, "/")
		if !ok || platform == "" || model == "" || strings.TrimSpace(key) != key || strings.TrimSpace(platform) != platform || strings.TrimSpace(model) != model {
			return fmt.Errorf("models.image_overflow_policies 的键必须为平台/模型名: %q", key)
		}
		if policy != ImageOverflowReject && policy != ImageOverflowTrimOldest {
			return fmt.Errorf("models.image_overflow_policies[%q] 必须为 reject 或 trim_oldest", key)
		}
	}
	return nil
}
