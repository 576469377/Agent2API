package zcode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"
)

const ProviderID = "agent2api"

type object = map[string]any

type Result struct {
	Status          string   `json:"status"`
	ModelIDs        []string `json:"model_ids"`
	ModelCount      int      `json:"model_count"`
	Added           []string `json:"added"`
	Removed         []string `json:"removed"`
	Retained        int      `json:"retained"`
	ProviderEnabled bool     `json:"provider_enabled"`
	DefaultChanged  bool     `json:"default_changed"`
	BackupPath      string   `json:"backup_path,omitempty"`
}

func parse(source []byte) (object, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(source, []byte{0xef, 0xbb, 0xbf})))
	decoder.UseNumber()
	var doc object
	if decoder.Decode(&doc) != nil || decoder.Decode(new(any)) != io.EOF || doc == nil {
		return nil, fmt.Errorf("ZCode 配置格式无效，未修改文件")
	}
	if doc["schemaVersion"] != json.Number("1") {
		return nil, fmt.Errorf("当前 ZCode 配置版本不支持一键同步，未修改文件")
	}
	if _, ok := doc["config"].(object); !ok {
		return nil, fmt.Errorf("ZCode 配置缺少有效 config，未修改文件")
	}
	return doc, nil
}

func child(parent object, key string) (object, error) {
	if value, exists := parent[key]; exists {
		if obj, ok := value.(object); ok {
			return obj, nil
		}
		return nil, fmt.Errorf("ZCode 配置结构无效（%s），未修改文件", key)
	}
	obj := object{}
	parent[key] = obj
	return obj, nil
}

func objects(parent object, key string) ([]any, error) {
	if value, exists := parent[key]; exists {
		list, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("ZCode 配置结构无效（%s），未修改文件", key)
		}
		for _, item := range list {
			if _, ok := item.(object); !ok {
				return nil, fmt.Errorf("ZCode 配置含无效规则，未修改文件")
			}
		}
		return list, nil
	}
	return []any{}, nil
}

func stringsIn(parent object, key string) ([]string, error) {
	if _, exists := parent[key]; !exists {
		return []string{}, nil
	}
	list, ok := parent[key].([]any)
	if !ok {
		return nil, fmt.Errorf("ZCode 配置含无效模型列表，未修改文件")
	}
	out := []string{}
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("ZCode 配置含无效模型标识，未修改文件")
		}
		out = append(out, s)
	}
	return out, nil
}

func unique(items []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func Validate(source []byte) (int, bool, error) {
	doc, err := parse(source)
	if err != nil {
		return 0, false, err
	}
	cfg := doc["config"].(object)
	providers, err := child(cfg, "providerConfigRules")
	if err != nil {
		return 0, false, err
	}
	rules, err := objects(providers, "providerRules")
	if err != nil {
		return 0, false, err
	}
	count, enabled := 0, true
	seen := false
	for _, item := range rules {
		p := item.(object)
		if p["providerId"] != ProviderID {
			continue
		}
		if seen {
			return 0, false, fmt.Errorf("ZCode 中有重复的 Agent2API 提供商，未修改文件")
		}
		seen = true
		config, err := child(p, "config")
		if err != nil {
			return 0, false, err
		}
		ids, err := stringsIn(config, "personalModelIds")
		if err != nil {
			return 0, false, err
		}
		count, enabled = len(ids), p["enabled"] != false
	}
	return count, enabled, nil
}

// Prepare 保留其他提供商、未知字段及仍免费的原有模型设置，仅修正本提供商的免费集合。
func Prepare(source []byte, models []Model, baseURL, key string, now time.Time) ([]byte, Result, error) {
	result := Result{Added: []string{}, Removed: []string{}}
	if _, _, err := Validate(source); err != nil {
		return nil, result, err
	}
	doc, _ := parse(source)
	original, _ := parse(source)
	byID := map[string]Model{}
	allowed := map[string]Model{}
	selected := []string{}
	for _, m := range models {
		id := route(m)
		byID[id] = m
		if m.DefaultRoute {
			byID[m.ID] = m
		}
		if Eligible(m, now) {
			allowed[id] = m
			selected = append(selected, id)
		}
	}
	selected = unique(selected)
	if len(selected) == 0 {
		return nil, result, fmt.Errorf("没有刚确认免费且公布上下文与输出上限的模型，未修改 ZCode 配置")
	}
	qualified := func(id string) string {
		if m, ok := byID[id]; ok {
			return route(m)
		}
		return id
	}
	cfg := doc["config"].(object)
	providerRoot, err := child(cfg, "providerConfigRules")
	if err != nil {
		return nil, result, err
	}
	providers, err := objects(providerRoot, "providerRules")
	if err != nil {
		return nil, result, err
	}
	var provider object
	for _, item := range providers {
		p := item.(object)
		if p["providerId"] == ProviderID {
			provider = p
		}
	}
	if provider == nil {
		provider = object{"providerId": ProviderID, "enabled": true}
		providers = append(providers, provider)
	}
	previous, err := child(provider, "config")
	if err != nil {
		return nil, result, err
	}
	oldIDs, err := stringsIn(previous, "personalModelIds")
	if err != nil {
		return nil, result, err
	}
	oldOrder, err := stringsIn(previous, "modelOrder")
	if err != nil {
		return nil, result, err
	}
	retained := []string{}
	oldSet := map[string]bool{}
	for _, old := range oldIDs {
		id := qualified(old)
		if _, ok := allowed[id]; ok {
			retained = append(retained, id)
			oldSet[id] = true
		} else {
			result.Removed = append(result.Removed, old)
		}
	}
	retained = unique(retained)
	merged := unique(append(append([]string{}, retained...), selected...))
	mergedSet := map[string]bool{}
	for _, id := range merged {
		mergedSet[id] = true
	}
	order := []string{}
	for _, id := range oldOrder {
		id = qualified(id)
		if mergedSet[id] {
			order = append(order, id)
		}
	}
	order = unique(append(order, merged...))
	for _, id := range selected {
		if !oldSet[id] {
			result.Added = append(result.Added, id)
		}
	}
	previous["personalModelIds"], previous["modelOrder"] = merged, order
	previous["group"] = "standard-personal"
	if key == "" {
		key = "agent2api-local"
	}
	previous["access"] = object{"type": "api-key", "apiKey": key}
	previous["api"] = object{"type": "anthropic-messages", "baseUrl": baseURL + "/v1"}
	provider["providerName"] = "Agent2API"
	if _, exists := provider["enabled"]; !exists {
		provider["enabled"] = true
	}
	providerRoot["providerRules"] = providers
	providerOrder, err := stringsIn(cfg, "providerOrder")
	if err != nil {
		return nil, result, err
	}
	foundProvider := false
	for _, id := range providerOrder {
		if id == ProviderID {
			foundProvider = true
		}
	}
	if !foundProvider {
		providerOrder = append(providerOrder, ProviderID)
	}
	cfg["providerOrder"] = providerOrder
	rules, err := child(cfg, "modelConfigRules")
	if err != nil {
		return nil, result, err
	}
	existing := map[string]bool{}
	disabled := map[string]bool{}
	for _, kind := range []string{"providerModelRules", "manualProviderModelRules"} {
		entries, err := objects(rules, kind)
		if err != nil {
			return nil, result, err
		}
		kept := []any{}
		for _, item := range entries {
			rule := item.(object)
			if rule["providerId"] != ProviderID {
				kept = append(kept, rule)
				continue
			}
			id, ok := rule["modelId"].(string)
			if !ok {
				return nil, result, fmt.Errorf("ZCode 模型规则缺少标识，未修改文件")
			}
			id = qualified(id)
			model, ok := allowed[id]
			if !ok {
				continue
			}
			if existing[id] {
				return nil, result, fmt.Errorf("模型前缀迁移会合并冲突设置，未修改 ZCode 配置")
			}
			existing[id] = true
			rule["modelId"] = id
			ruleConfig, err := child(rule, "config")
			if err != nil {
				return nil, result, err
			}
			if ruleConfig["enabled"] == false {
				disabled[id] = true
			}
			if err := adjustOptions(ruleConfig, model); err != nil {
				return nil, result, err
			}
			kept = append(kept, rule)
		}
		rules[kind] = kept
	}
	for _, id := range merged {
		if !existing[id] {
			rules["manualProviderModelRules"] = append(rules["manualProviderModelRules"].([]any), newRule(allowed[id]))
			existing[id] = true
		}
	}
	if value, exists := cfg["defaultModelSelection"]; exists && value != nil {
		selection, ok := value.(object)
		if !ok {
			return nil, result, fmt.Errorf("ZCode 默认模型设置无效，未修改文件")
		}
		if selection["providerId"] == ProviderID {
			id, ok := selection["modelId"].(string)
			if !ok {
				return nil, result, fmt.Errorf("ZCode 默认模型缺少标识，未修改文件")
			}
			id = qualified(id)
			if mergedSet[id] {
				selection["modelId"] = id
			} else {
				result.DefaultChanged = true
				delete(cfg, "defaultModelSelection")
				if provider["enabled"] != false {
					for _, candidate := range order {
						if !disabled[candidate] {
							cfg["defaultModelSelection"] = object{"providerId": ProviderID, "modelId": candidate}
							break
						}
					}
				}
			}
		}
	}
	result.ModelIDs, result.ModelCount, result.Retained, result.ProviderEnabled = merged, len(merged), len(retained), provider["enabled"] != false
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, result, fmt.Errorf("无法生成 ZCode 配置，未修改文件")
	}
	// 用重新解析后的结构比较，避免 []string 与 []any 的表示差异导致重复写入。
	canonical, _ := parse(encoded)
	if reflect.DeepEqual(original, canonical) {
		result.Status = "unchanged"
		return source, result, nil
	}
	result.Status = "updated"
	return append(encoded, '\n'), result, nil
}

func adjustOptions(config object, model Model) error {
	if model.Platform != "openrouter" && model.Platform != "cursor" && model.Platform != "trae-cn" && model.Platform != "kiro" && model.Platform != "kilo" {
		return nil
	}
	specs, err := child(config, "optionSpecs")
	if err != nil {
		return err
	}
	if model.Platform == "kiro" {
		specs["reasoningLevel"] = object{"values": []string{"disabled"}, "map": "{}"}
	}
	if model.Platform == "cursor" || model.Platform == "trae-cn" {
		specs["reasoningLevel"] = object{"values": []string{"enabled"}, "map": "{}"}
		if model.Platform == "trae-cn" && (model.ID == "deepseek-v4.1-flash" || model.ID == "glm-5.3-flash") && model.ContextTokens == 1000000 && model.MaxOutputTokens == 64000 {
			props, err := child(config, "properties")
			if err != nil {
				return err
			}
			// 早期同步从 solo_work_lite 写入了这对默认值；升级已验证的
			// 1M 档位，同时保留其他手动上下文和更小的输出预算。
			if props["contextWindow"] == json.Number("200000") {
				props["contextWindow"] = model.ContextTokens
				maximum, err := child(specs, "maxOutputTokens")
				if err != nil {
					return err
				}
				if maximum["max"] == json.Number("32000") {
					maximum["max"] = outputLimit(model)
				}
			}
		}
		return nil
	}
	maximum, err := child(specs, "maxOutputTokens")
	if err != nil {
		return err
	}
	if value, exists := maximum["max"]; exists {
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("ZCode 输出上限设置无效，未修改文件")
		}
		amount, err := number.Float64()
		if err != nil {
			return fmt.Errorf("ZCode 输出上限设置无效，未修改文件")
		}
		if amount <= float64(outputLimit(model)) {
			return nil
		}
	}
	maximum["max"] = outputLimit(model)
	return nil
}

func newRule(model Model) object {
	values := []string{"disabled"}
	if model.SupportsThinking {
		values = append(values, "enabled")
	}
	reasoning := object{"values": values, "map": `reasoningLevel == "disabled" ? {"thinking":{"type":"disabled"}} : {"thinking":{"type":"adaptive"},"output_config":{"effort": reasoningLevel == "enabled" ? "high" : reasoningLevel}}`}
	if model.Platform == "cursor" || model.Platform == "trae-cn" {
		reasoning = object{"values": []string{"enabled"}, "map": "{}"}
	}
	if model.Platform == "kiro" {
		reasoning = object{"values": []string{"disabled"}, "map": "{}"}
	}
	return object{"providerId": ProviderID, "modelId": route(model), "config": object{
		"enabled": true,
		"properties": object{"contextWindow": model.ContextTokens, "supportsJsonSchemaOutput": false, "supportsNativeWebSearch": false, "supportsMidConversationSystem": false,
			"inputFormat": object{"supportsImage": model.SupportsImages, "supportsVideo": false, "supportsPdf": false}},
		"optionSpecs": object{"reasoningLevel": reasoning, "maxOutputTokens": object{"max": outputLimit(model)}},
	}}
}
