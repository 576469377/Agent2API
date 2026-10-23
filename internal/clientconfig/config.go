package clientconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

type object = map[string]any

type Result struct {
	Status        string   `json:"status"`
	ModelIDs      []string `json:"model_ids"`
	ModelCount    int      `json:"model_count"`
	ProviderCount int      `json:"provider_count"`
	Added         []string `json:"added"`
	Removed       []string `json:"removed"`
	Retained      int      `json:"retained"`
	BackupPath    string   `json:"backup_path,omitempty"`
	Apps          []string `json:"apps,omitempty"`
}

func parse(raw []byte) (object, error) {
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})))
	d.UseNumber()
	var doc object
	if d.Decode(&doc) != nil || d.Decode(new(any)) != io.EOF || doc == nil {
		return nil, fmt.Errorf("配置不是有效的 JSON 对象，原文件已保留")
	}
	return doc, nil
}

func child(doc object, key string) (object, error) {
	if v, exists := doc[key]; exists {
		if obj, ok := v.(object); ok {
			return obj, nil
		}
		return nil, fmt.Errorf("配置字段 %s 格式无效，原配置已保留", key)
	}
	obj := object{}
	doc[key] = obj
	return obj, nil
}

func selected(models []Model, platforms []string, now time.Time) ([]Model, error) {
	wanted := map[string]bool{}
	for _, p := range platforms {
		wanted[p] = true
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("请至少选择一个上游平台")
	}
	out, seen, found := []Model{}, map[string]bool{}, map[string]bool{}
	for _, m := range models {
		if !wanted[m.Platform] || !Eligible(m, now) || !m.SupportsTools || seen[Route(m)] {
			continue
		}
		seen[Route(m)] = true
		found[m.Platform] = true
		out = append(out, m)
	}
	for p := range wanted {
		if !found[p] {
			return nil, fmt.Errorf("平台 %s 暂无刚核验、支持工具调用且预算可配置的免费模型，未写入配置", p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return Route(out[i]) < Route(out[j]) })
	return out, nil
}

func newResult(models []Model) Result {
	r := Result{Status: "unchanged", ModelIDs: []string{}, Added: []string{}, Removed: []string{}}
	for _, m := range models {
		r.ModelIDs = append(r.ModelIDs, Route(m))
	}
	r.ModelCount = len(r.ModelIDs)
	return r
}

func PlatformName(id string) string {
	if name := map[string]string{"workbuddy": "WorkBuddy 国内", "workbuddy-global": "WorkBuddy 国际", "openrouter": "OpenRouter", "cursor": "Cursor", "trae-cn": "TRAE 国内", "kiro": "Kiro Free"}[id]; name != "" {
		return name
	}
	return id
}
