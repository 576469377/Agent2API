// Package llm 定义供应商无关的中间表示（IR）。
//
// 本包是整个项目的枢纽：它不 import 任何上游适配包，也不 import 任何下游协议包。
// 上游适配器（internal/adapter/*）与下游协议编解码器（internal/api/*）只通过本包通信，
// 从而把「N 个平台 × M 个协议」的复杂度从 N×M 降为 N+M。
//
// 设计准则：IR 表达的是 agent loop 的语义等价，而不是某个上游的请求结构等价。
// 一旦本包出现「// XXX 上游要求...」这样的注释，就是抽象开始泄漏的信号。
package llm

// Role 是消息角色。
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "toolResult"
)

// ContentType 是内容块类型。
type ContentType string

const (
	ContentText       ContentType = "text"
	ContentThinking   ContentType = "thinking"
	ContentImage      ContentType = "image"
	ContentToolCall   ContentType = "toolCall"
	ContentToolResult ContentType = "toolResult"
)

// ToolCall 是一次工具调用请求。
type ToolCall struct {
	ID        string // 调用 ID，用于把工具结果关联回调用
	Name      string
	Arguments string // JSON 字符串
}

// ToolResult 是一次工具调用的结果。
type ToolResult struct {
	ToolCallID string
	// Content 是纯文本结果，保留以兼容仅含文本的工具返回。
	Content string
	// Blocks 是结构化结果（文本 + 图片）。Claude Code 的 Read 工具读取图片后，
	// 会把图片作为 image 块嵌进 tool_result；这里必须原样保留，否则图片丢失、
	// 模型无法「看图」。仅当结果含图片时才需要用到它。
	Blocks  []Content
	IsError bool
}

// Content 是一个内容块。用标签联合而非接口，避免为每种组合写一套方法。
type Content struct {
	Type ContentType

	// ContentText / ContentThinking 的载荷
	Text     string
	Thinking string
	// 加密思考签名等「看不懂但必须原样回传」的不透明数据
	ThinkingSignature string

	// ContentImage 的载荷：data: URL 或 http(s) URL
	ImageURL string

	// ContentToolCall / ContentToolResult 的载荷
	ToolCall   *ToolCall
	ToolResult *ToolResult
}

// Message 是会话中的一条消息。
type Message struct {
	Role    Role
	Content []Content

	// RoleTool 时必填：关联的工具调用 ID
	ToolCallID string
}

// ToolDefinition 是工具定义。Parameters 是 JSON Schema。
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolChoiceMode 是工具选择模式。
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceFunction ToolChoiceMode = "function"
)

// ToolChoice 描述工具选择策略。
type ToolChoice struct {
	Mode         ToolChoiceMode
	FunctionName string // Mode == ToolChoiceFunction 时有效
}

// Usage 是 token 用量。
type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	CachedTokens    int
	TotalTokens     int
}

// RequestMessages 是发送给任意上游适配器的完整请求上下文。
type RequestMessages struct {
	Model        string
	SystemPrompt string // 独立于消息历史
	Messages     []Message

	Tools       []ToolDefinition
	ToolChoice  *ToolChoice
	MaxTokens   *int
	Temperature *float64
	TopP        *float64
	TopK        *int
	Stop        []string
	Seed        *int64

	// ReasoningEffort: "low" / "medium" / "high"，空表示不指定
	ReasoningEffort string
	// JSONMode 要求输出合法 JSON
	JSONMode bool

	Stream       bool
	IncludeUsage bool

	// Dropped 记录解码期被丢弃或降级的下游字段，用于可观测性。
	Dropped []string

	// SessionKey 是会话亲和的**路由键**——同一会话的多次请求应落同一账号。
	//
	// 注意它与「会话 ID」是两回事：显式信号（metadata.user_id 等）优先；
	// 没有显式信号时用「系统提示+首条 user 消息」的哈希兜底——它们在会话
	// 内稳定，而完整消息列表逐轮增长，直接哈希会每轮变化。
	// 空串表示无法确定会话（如纯 API 单轮调用），不做亲和。
	SessionKey string
}
