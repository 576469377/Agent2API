package clientconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

func PiPath() string {
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return filepath.Join(dir, "models.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent", "models.json")
}

func piDoc(source []byte) (object, object, error) {
	doc := object{}
	var err error
	if source != nil {
		doc, err = parse(source)
		if err != nil {
			return nil, nil, err
		}
	}
	providers, err := child(doc, "providers")
	return doc, providers, err
}

func ValidatePi(source []byte) (int, error) {
	_, providers, err := piDoc(source)
	if err != nil {
		return 0, err
	}
	count := 0
	for id, value := range providers {
		if !strings.HasPrefix(id, "agent2api-") {
			continue
		}
		p, ok := value.(object)
		if !ok {
			return 0, fmt.Errorf("pi 的 Agent2API 供应商格式无效")
		}
		models, err := piModels(p)
		if err != nil {
			return 0, err
		}
		count += len(models)
	}
	return count, nil
}

func PreparePi(source []byte, models []Model, platforms []string, baseURL, key string, now time.Time) ([]byte, Result, error) {
	chosen, err := selected(models, platforms, now)
	if err != nil {
		return nil, Result{}, err
	}
	doc, providers, err := piDoc(source)
	if err != nil {
		return nil, Result{}, err
	}
	result := newResult(chosen)
	groups := map[string][]Model{}
	for _, m := range chosen {
		groups[m.Platform] = append(groups[m.Platform], m)
	}
	if key == "" {
		key = "agent2api-local"
	}
	if strings.HasPrefix(key, "!") || strings.Contains(key, "$") {
		return nil, Result{}, fmt.Errorf("网关密钥含 pi 会解释为命令或环境插值的字符，请调整网关密钥后配置")
	}
	for platform, group := range groups {
		id := "agent2api-" + platform
		p, err := child(providers, id)
		if err != nil {
			return nil, Result{}, err
		}
		// 未经本工具生成的同名供应商不可静默接管。
		if len(p) > 0 && (p["api"] != "openai-completions" || !localBase(p["baseUrl"])) {
			return nil, Result{}, fmt.Errorf("pi 中同名供应商 %s 与本机网关配置冲突，原配置已保留", id)
		}
		old, err := piModels(p)
		if err != nil {
			return nil, Result{}, err
		}
		p["baseUrl"], p["api"], p["apiKey"], p["authHeader"] = strings.TrimRight(baseURL, "/")+"/v1", "openai-completions", key, true
		if _, exists := p["name"]; !exists {
			p["name"] = "Agent2API · " + PlatformName(platform)
		}
		list, keep := []any{}, map[string]bool{}
		for _, m := range group {
			route := Route(m)
			item, exists := old[route]
			if !exists {
				item = object{"id": route, "name": m.DisplayName}
				if m.DisplayName == "" {
					item["name"] = m.ID
				}
				result.Added = append(result.Added, route)
			} else {
				result.Retained++
			}
			keep[route] = true
			for field, limit := range map[string]int{"contextWindow": m.ContextTokens, "maxTokens": OutputLimit(m)} {
				if previous, ok := item[field].(json.Number); ok {
					if n, err := previous.Int64(); err == nil && n > 0 && n < int64(limit) {
						limit = int(n)
					}
				}
				item[field] = limit
			}
			// 固定思考的平台无法兑现客户端的开关或强度设置。
			item["reasoning"] = m.SupportsThinking && platform != "cursor" && platform != "trae-cn"
			item["input"] = []string{"text"}
			if m.SupportsImages {
				item["input"] = []string{"text", "image"}
			}
			item["cost"] = object{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}
			compat, err := child(item, "compat")
			if err != nil {
				return nil, Result{}, err
			}
			compat["supportsDeveloperRole"], compat["supportsStore"], compat["maxTokensField"] = false, false, "max_tokens"
			compat["supportsReasoningEffort"] = item["reasoning"]
			// 移除旧连接覆盖，保证密钥只发往刚核验的本机网关。
			delete(item, "baseUrl")
			delete(item, "api")
			delete(item, "headers")
			list = append(list, item)
		}
		delete(p, "headers")
		delete(p, "oauth")
		for route := range old {
			if !keep[route] {
				result.Removed = append(result.Removed, route)
			}
		}
		p["models"] = list
		result.ProviderCount++
	}
	sort.Strings(result.Added)
	sort.Strings(result.Removed)
	candidate, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, Result{}, err
	}
	candidate = append(candidate, '\n')
	previous, _, _ := piDoc(source)
	current, _, _ := piDoc(candidate)
	if source != nil && reflect.DeepEqual(previous, current) {
		return source, result, nil
	}
	result.Status = "updated"
	return candidate, result, nil
}

func piModels(p object) (map[string]object, error) {
	out := map[string]object{}
	value, exists := p["models"]
	if !exists {
		return out, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("pi 模型列表格式无效")
	}
	for _, v := range list {
		item, ok := v.(object)
		id, _ := item["id"].(string)
		if !ok || id == "" || out[id] != nil {
			return nil, fmt.Errorf("pi 模型列表含无效或重复条目")
		}
		out[id] = item
	}
	return out, nil
}
