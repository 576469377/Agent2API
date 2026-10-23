package trae

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

type modelConfig struct {
	Name      string         `json:"config_name"`
	Source    int            `json:"config_source"`
	Enabled   bool           `json:"config_switch"`
	Invisible bool           `json:"is_invisible_to_user"`
	Usage     string         `json:"usage"`
	Context   map[string]int `json:"context_window_tokens"`
	Contact   string         `json:"display_contact_config"`
	Display   struct {
		Name   string `json:"display_name"`
		Custom bool   `json:"is_custom_model"`
		Max    bool   `json:"max_mode"`
	} `json:"display_config"`
	Details []struct {
		Name string `json:"model_name"`
		Max  int    `json:"max_tokens"`
	} `json:"model_detail_list"`
}

type catalogModel struct {
	Info     adapter.ModelInfo
	WireName string
	Source   int
}

// 首版只发布逐一验证的普通免费账号模型；目录中的模板、子 Agent、会员模型不透传。
var enabledModels = map[string]bool{"deepseek-v4.1-flash": true, "glm-5.3-flash": true, "Doubao-Seed-2.1-Turbo": true}

func decodeModels(raw []byte, remaining float64, now time.Time) ([]catalogModel, error) {
	var result struct {
		Models []modelConfig `json:"config_info_list"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return nil, fault("trae_invalid_catalog", "TRAE 模型目录格式无效")
	}
	out := []catalogModel{}
	stamp := now.UTC().Format(time.RFC3339)
	seen := map[string]bool{}
	for _, m := range result.Models {
		if !enabledModels[m.Name] || seen[m.Name] || !m.Enabled || m.Invisible || m.Usage != "chat_completion" || m.Source != 1 || m.Display.Custom || m.Context["dev"] <= 0 {
			continue
		}
		var contact struct {
			Access struct {
				Data struct {
					Identities []int `json:"identity_list"`
				} `json:"data"`
			} `json:"access"`
			Rate struct {
				Enabled bool `json:"enable"`
				Data    struct {
					Rate *float64 `json:"rate"`
				} `json:"data"`
			} `json:"consumption_rate"`
		}
		if json.Unmarshal([]byte(m.Contact), &contact) != nil || !contact.Rate.Enabled || contact.Rate.Data.Rate == nil || !finite(*contact.Rate.Data.Rate) || *contact.Rate.Data.Rate < 0 {
			continue
		}
		free := false
		for _, id := range contact.Access.Data.Identities {
			if id == 0 {
				free = true
			}
		}
		if !free {
			continue
		}
		// solo_work_lite 仅公布普通窗口；solo_agent 同时给出 dev/max。
		// 仅在目录明确支持且存在完整配对时使用更大窗口及其真实 wire model。
		variant, context := "dev", m.Context["dev"]
		if m.Display.Max && m.Context["max"] > context {
			for _, d := range m.Details {
				if strings.HasSuffix(d.Name, "__max") && d.Max > 0 && d.Max < m.Context["max"] {
					variant, context = "max", m.Context["max"]
					break
				}
			}
		}
		for _, d := range m.Details {
			if !strings.HasSuffix(d.Name, "__"+variant) || d.Max <= 0 || d.Max >= context {
				continue
			}
			rate := *contact.Rate.Data.Rate
			info := adapter.ModelInfo{ID: m.Name, DisplayName: m.Display.Name, Description: "个人 Free 月赠与已核验签到积分；支持文本、原生工具与思考输出；不支持图片、JSON 模式、stop；max_tokens、采样和思考开关不生效，输出上限仅为上游目录值", ContextTokens: context, MaxOutputTokens: d.Max, SupportsTools: true, SupportsThinking: true, IsDefault: m.Name == "deepseek-v4.1-flash",
				Pricing: &adapter.ModelPricing{Status: "credits", CreditMultiplier: &rate, Source: "trae_account_catalog", ObservedAt: stamp, UpstreamLabel: "上游积分消耗倍率；由已核验的个人 Free 赠送积分支付"},
				Access:  &adapter.ModelAccess{Basis: "recurring_free_allowance", Plan: "free", OverageEnabled: false, RemainingCredits: remaining, Source: "trae_free_plan", CheckedAt: stamp}}
			if info.DisplayName == "" {
				info.DisplayName = m.Name
			}
			out = append(out, catalogModel{Info: info, WireName: d.Name, Source: m.Source})
			seen[m.Name] = true
			break
		}
	}
	if len(out) == 0 {
		return nil, fault("trae_no_verified_models", "当前 TRAE 目录未提供已验证的免费账号模型")
	}
	return out, nil
}
