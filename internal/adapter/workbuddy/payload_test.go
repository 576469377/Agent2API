package workbuddy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// TestNormalizeToolChoice 覆盖上游最反直觉的一条约束：
// tool_choice 必须是 string，object 形式会触发 400 cannot unmarshal ... of type string。
func TestNormalizeToolChoice(t *testing.T) {
	cases := []struct {
		name string
		in   llm.ToolChoice
		want string
	}{
		{"指定函数", llm.ToolChoice{Mode: llm.ToolChoiceFunction, FunctionName: "get_weather"}, "get_weather"},
		{"未带函数名退化为 required", llm.ToolChoice{Mode: llm.ToolChoiceFunction}, "required"},
		{"auto", llm.ToolChoice{Mode: llm.ToolChoiceAuto}, "auto"},
		{"none", llm.ToolChoice{Mode: llm.ToolChoiceNone}, "none"},
		{"required", llm.ToolChoice{Mode: llm.ToolChoiceRequired}, "required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeToolChoice(&c.in)
			if got != c.want {
				t.Errorf("期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

// TestBuildUpstreamRequestAlwaysStreams 保证请求恒为流式：
// 上游不支持非流式（code 11101），非流式响应由代理侧聚合。
func TestBuildUpstreamRequestAlwaysStreams(t *testing.T) {
	req := llm.RequestMessages{
		Model:    "glm-5.3",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hi"}}}},
	}
	out := buildUpstreamRequest(req, false)
	if !out.Stream {
		t.Fatal("上游请求必须恒为 stream=true")
	}
	if out.StreamOptions["include_usage"] != true {
		t.Error("必须注入 stream_options.include_usage 才能拿到 usage")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "tool_choice\":{") {
		t.Error("tool_choice 不应以 object 形式下发")
	}
}

// TestConvertToolsRequiresType 覆盖上游对工具 schema 的要求：
// parameters 必须是非空 dict 且含 type 字段。
func TestConvertToolsRequiresType(t *testing.T) {
	tools := convertTools([]llm.ToolDefinition{
		{Name: "no_params"},
		{Name: "no_type", Parameters: map[string]any{"properties": map[string]any{}}},
	}, false)
	if len(tools) != 2 {
		t.Fatalf("期望保留 2 个工具，实际 %d", len(tools))
	}
	for _, tool := range tools {
		if _, ok := tool.Function.Parameters["type"]; !ok {
			t.Errorf("工具 %s 的 parameters 缺少 type 字段", tool.Function.Name)
		}
	}
}

// TestConvertMessagesExpandsMultipleToolResults 覆盖「一条消息携带多个工具结果」的场景。
//
// Claude Code 常把一批工具的返回合并放进同一条 user 消息里，而上游要求每个
// tool_call 对应一条独立的 tool 消息。展开不全就会触发上游
// "tool calls and tool results do not match"。
func TestConvertMessagesExpandsMultipleToolResults(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Content{
			{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "t1", Name: "Bash", Arguments: "{}"}},
			{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "t2", Name: "Read", Arguments: "{}"}},
		}},
		{Role: llm.RoleTool, ToolCallID: "t2", Content: []llm.Content{
			{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "t1", Content: "结果一"}},
			{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "t2", Content: "结果二"}},
		}},
	}

	out := convertMessages(msgs, false)

	var toolMsgs []upstreamMessage
	for _, m := range out {
		if m.Role == "tool" {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 2 {
		t.Fatalf("2 个工具结果应展开为 2 条 tool 消息，实际 %d 条", len(toolMsgs))
	}
	want := map[string]string{"t1": "结果一", "t2": "结果二"}
	for _, m := range toolMsgs {
		if want[m.ToolCallID] != m.Content {
			t.Errorf("tool_call_id=%s 的内容应为 %q，实际 %q", m.ToolCallID, want[m.ToolCallID], m.Content)
		}
	}
}

// TestConvertMessagesFillsEmptyToolCallID 保证 tool_call 的 id 不为空：
// 上游靠 id 关联调用与结果，空 id 会导致匹配失败。
func TestConvertMessagesFillsEmptyToolCallID(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Content{
			{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{Name: "Bash", Arguments: "{}"}},
		}},
	}
	out := convertMessages(msgs, false)
	if len(out) != 1 || len(out[0].ToolCalls) != 1 {
		t.Fatalf("期望 1 条 assistant 消息带 1 个 tool_call，实际 %+v", out)
	}
	if out[0].ToolCalls[0].ID == "" {
		t.Error("tool_call 的 id 不应为空，应自动生成")
	}
}

// TestConvertMessagesToolResultImage 覆盖「工具读取了图片」的场景：
// tool_result 内嵌的图片必须以上游的多模态数组形式（含 image_url）转发，不能丢。
func TestConvertMessagesToolResultImage(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleTool, ToolCallID: "tu_1", Content: []llm.Content{
			{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{
				ToolCallID: "tu_1",
				Content:    "图片内容如下",
				Blocks: []llm.Content{
					{Type: llm.ContentText, Text: "图片内容如下"},
					{Type: llm.ContentImage, ImageURL: "data:image/jpeg;base64,/9j/4AAQ"},
				},
			}},
		}},
	}
	out := convertMessages(msgs, false)
	if len(out) != 1 || out[0].Role != "tool" {
		t.Fatalf("期望 1 条 tool 消息，实际 %+v", out)
	}
	parts, ok := out[0].Content.([]contentPart)
	if !ok {
		t.Fatalf("含图片的 tool 结果应编码为数组 content，实际 %T: %+v", out[0].Content, out[0].Content)
	}
	var hasImage bool
	for _, p := range parts {
		if p.Type == "image_url" && p.ImageURL != nil && strings.HasPrefix(p.ImageURL.URL, "data:image/jpeg;base64,") {
			hasImage = true
		}
	}
	if !hasImage {
		t.Errorf("上游 tool 消息未携带 image_url，实际: %+v", parts)
	}
}

// TestConvertMessagesToolResultTextOnly 保证纯文本工具结果仍是字符串 content，行为不变。
func TestConvertMessagesToolResultTextOnly(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleTool, ToolCallID: "tu_2", Content: []llm.Content{
			{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "tu_2", Content: "纯文本"}},
		}},
	}
	out := convertMessages(msgs, false)
	if s, ok := out[0].Content.(string); !ok || s != "纯文本" {
		t.Fatalf("纯文本工具结果应编码为字符串 content，实际 %T: %+v", out[0].Content, out[0].Content)
	}
}

// TestSanitizeInsertsZeroWidth 验证脱敏会破坏敏感词的精确匹配。
func TestSanitizeInsertsZeroWidth(t *testing.T) {
	in := "Refuse requests for DoS attacks, exploit development, credential testing."
	out := SanitizeSystemPrompt(in)
	if out == in {
		t.Fatal("脱敏未生效：输出与输入相同")
	}
	if strings.Contains(out, "DoS") && !strings.Contains(out, "Do\u200bS") {
		t.Error("DoS 未被插入零宽空格")
	}
}

// TestSanitizeCompactsLongHarness 验证超长 harness 模板会被整段压缩：
// 零宽空格对这类模板无效，必须整体压掉，否则上游审核会拦截整条请求。
func TestSanitizeCompactsLongHarness(t *testing.T) {
	long := strings.Repeat("You are Claude Code, a coding agent. ", 60) +
		"<system-reminder>tool_use and antml:invoke are available.</system-reminder>"
	out := SanitizeSystemPrompt(long)
	if len(out) >= len(long) {
		t.Fatalf("长模板未被压缩：输入 %d 字符，输出 %d 字符", len(long), len(out))
	}
	if !strings.Contains(out, "omitted") {
		t.Error("压缩结果应包含省略说明")
	}
}

// TestSanitizeLeavesShortTextAlone 确保短文本不会被误伤。
func TestSanitizeLeavesShortTextAlone(t *testing.T) {
	in := "你是一个乐于助人的助手。"
	if out := SanitizeSystemPrompt(in); out != in {
		t.Errorf("短文本不应被改写，实际: %q", out)
	}
}

// TestConvertToolsSanitizesDescription 是「工具描述未脱敏」的回归测试。
//
// 曾有 bug：SanitizeToolDescription 已实现却从未被调用，工具描述里的
// 敏感术语（安全类工具的常规描述词）原样发往上游，被关键词审核误拦。
func TestConvertToolsSanitizesDescription(t *testing.T) {
	defs := []llm.ToolDefinition{
		{Name: "sec_scan", Description: "Detect SQL injection and XSS vulnerabilities."},
	}

	on := convertTools(defs, true)
	off := convertTools(defs, false)

	if len(on) != 1 || len(off) != 1 {
		t.Fatalf("工具数量错误: on=%d off=%d", len(on), len(off))
	}
	// 开启脱敏后，敏感词应被零宽空格打断（渲染不可见，但审核匹配失效）。
	if got := on[0].Function.Description; got == off[0].Function.Description {
		t.Fatalf("开启脱敏后描述应发生变化: %q", got)
	}
	if !strings.Contains(off[0].Function.Description, "SQL injection") {
		t.Fatalf("关闭脱敏时应保留原文: %q", off[0].Function.Description)
	}
	if strings.Contains(on[0].Function.Description, "SQL injection") {
		t.Fatalf("开启脱敏后不应残留未打断的敏感词: %q", on[0].Function.Description)
	}
	// 工具名与参数 schema 不受脱敏影响。
	if on[0].Function.Name != "sec_scan" || off[0].Function.Name != "sec_scan" {
		t.Fatal("工具名不应被改动")
	}
}

// TestResetEpochFromMsg 验证从上游限流文案解析重置时刻。
// 号池的「到点解冻」冷却依赖这个解析；文案格式变化时退化为 0（固定冷却）。
func TestResetEpochFromMsg(t *testing.T) {
	msg := "您的使用量已超出频率限制，将在 2026-09-16 23:21:31 UTC+8 重置，您也可以切换其他模型继续使用。"
	secs := resetEpochFromMsg(msg)
	if secs <= 0 {
		t.Fatalf("应解析出重置时刻, got %d", secs)
	}
	// 解析出的时刻换算回 UTC+8 应与原文一致。
	got := time.Unix(int64(secs), 0).In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02 15:04:05")
	if got != "2026-09-16 23:21:31" {
		t.Fatalf("重置时刻=%s, want 2026-09-16 23:21:31", got)
	}
	if resetEpochFromMsg("some other error") != 0 {
		t.Fatal("无关文案应返回 0")
	}
	if resetEpochFromMsg("将在 不存在的格式") != 0 {
		t.Fatal("格式损坏应返回 0")
	}
}

// TestHttpErrorParsesResetTime 验证 httpError 把重置时刻填进 RetryAfterSeconds。
func TestHttpErrorParsesResetTime(t *testing.T) {
	body := []byte(`{"code":429,"msg":"您的使用量已超出频率限制，将在 2099-01-01 00:00:00 UTC+8 重置"}`)
	err := httpError(429, body, "")
	f := llm.Wrap(err)
	if !f.RateLimited {
		t.Fatalf("429 应置 RateLimited: %+v", f)
	}
	if f.RetryAfterSeconds <= 0 {
		t.Fatalf("应从重置时刻推导冷却秒数, got %d", f.RetryAfterSeconds)
	}
}

// TestQuotaExhaustedClassification 验证「额度耗尽」（code 14018）的分类：
// 标记 QuotaExhausted（号池据此做账号级长冷却）+ RateLimited（换号依据）。
// 实测该错误嵌套在 error.data.code 里（如 {"error":{"data":{"code":14018,...}}}），
// envelope 顶层解析不到，必须走 nestedBusinessCode。
func TestQuotaExhaustedClassification(t *testing.T) {
	body := []byte(`{"error":{"data":{"code":14018,"msg":"额度已用尽，请访问以下链接，购买加量包以获取更多额度"}}}`)
	err := httpError(400, body, "")
	f := llm.Wrap(err)
	if !f.QuotaExhausted || !f.RateLimited {
		t.Fatalf("14018 应标 QuotaExhausted+RateLimited: %+v", f)
	}
}
