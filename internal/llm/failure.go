package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// Failure 是请求失败的分类记录，实现 error 接口。
//
// 核心思想：错误在还有类型信息的时候（生产侧）填结构字段，之后全程携带结构体，
// 绝不压平成字符串再让呈现层用正则反推语义。
type Failure struct {
	// —— 生产侧填 ——
	Code    string // 上游错误码，如 WorkBuddy 的 "11101"
	Message string
	Cause   error

	// UpstreamFault 表示责任在上游：传输断裂、内部故障伪装成可修正的 code。
	// 它的语义压过 Code —— 否则会把上游的锅甩给调用方、把传输断裂当成限流去冷却。
	UpstreamFault bool

	// RetryAfterSeconds 是上游建议的重试等待秒数。
	RetryAfterSeconds int

	// —— 派生分类 ——
	RateLimited   bool
	ContextLength bool
	Timeout       bool
	Canceled      bool
	// QuotaExhausted 表示账号额度耗尽（如需购买加量包），不会自动重置。
	// 与 RateLimited（到点重置、可精确冷却）语义不同：它应当触发
	// **账号级**的长冷却，而不是按模型分别试错。
	QuotaExhausted bool
	// Unauthorized 表示凭据缺失或错误。必须独立于 ClientFixable：
	// HTTP 层要求这类失败返回 401（客户端 SDK 靠 401 触发重新配置凭据），
	// 而 ClientFixable 会被统一映射成 400，掩盖真实原因。
	Unauthorized bool
	// ClientFixable 表示调用方修改请求后可以成功。
	ClientFixable bool
}

// Error 实现 error 接口。保持 "<code>: <msg>" 格式以便日志检索。
func (f *Failure) Error() string {
	if f == nil {
		return "<nil>"
	}
	if f.Code == "" {
		return f.Message
	}
	return f.Code + ": " + f.Message
}

// Unwrap 保留 errors.Is / errors.As 链。
func (f *Failure) Unwrap() error { return f.Cause }

// NewFailure 构造一个失败记录并立即派生分类。
func NewFailure(code, message string, cause error) *Failure {
	f := &Failure{Code: code, Message: message, Cause: cause}
	f.classify()
	return f
}

// Wrap 把任意 error 包成 *Failure；本身已是 *Failure 则补齐派生分类后返回。
//
// 补分类是必要的：调用方常直接构造 Failure 字面量（只填 Code/Message/Cause
// 等生产侧字段）再交给 Wrap，若原样返回，派生字段（Timeout/RateLimited…）
// 会全部停留在零值——超时被判成通用上游故障、HTTPStatus 给出 502 而非 504。
// classify 只依据生产侧字段重新推导派生字段，重复调用是幂等的。
func Wrap(err error) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		if f != nil {
			f.Classify()
		}
		return f
	}
	if err == nil {
		return nil
	}
	f = &Failure{Code: "internal_error", Message: err.Error(), Cause: err}
	f.classify()
	return f
}

// Classify 重新派生分类字段。供外部直接构造 Failure 字面量后补齐使用。
func (f *Failure) Classify() *Failure {
	f.classify()
	return f
}

// classify 按错误文本与底层类型派生分类字段。
func (f *Failure) classify() {
	msg := strings.ToLower(f.Message)
	if f.Cause != nil {
		msg += " " + strings.ToLower(f.Cause.Error())
	}

	// 文本匹配的兜底。刻意不匹配裸数字 "401"/"429"：错误消息常回显请求体
	// 片段或含 "user_401" 这类 ID，裸数字会把 decode_failed 之类的错误
	// 误判成鉴权/限流失败（连 HTTP 状态码都被带偏）。可靠的信号只有两种：
	// 1) 生产方显式设置——httpError 对上游 401/429 显式置位；
	// 2) 语义化词组——"unauthorized"/"rate limit"/"too many requests"/中文同义。

	switch {
	case errors.Is(f.Cause, context.Canceled):
		f.Canceled = true
		f.ClientFixable = true
		return
	}
	if f.Cause != nil {
		var netErr net.Error
		if errors.As(f.Cause, &netErr) && netErr.Timeout() {
			f.Timeout = true
		}
		if errors.Is(f.Cause, syscall.ECONNRESET) || errors.Is(f.Cause, syscall.ECONNREFUSED) {
			f.UpstreamFault = true
		}
	}

	switch {
	case strings.Contains(msg, "14018"), strings.Contains(msg, "额度已用尽"),
		strings.Contains(msg, "购买加量包"):
		f.QuotaExhausted = true
		f.RateLimited = true
		f.ClientFixable = false
		return
	case strings.Contains(msg, "context length"), strings.Contains(msg, "too long"),
		strings.Contains(msg, "maximum context"), strings.Contains(msg, "token limit"):
		f.ContextLength = true
		f.ClientFixable = true
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many request"),
		strings.Contains(msg, "quota"), strings.Contains(msg, "rate limited"):
		f.RateLimited = true
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "timeout"),
		strings.Contains(msg, "timed out"):
		f.Timeout = true
	case strings.Contains(msg, "invalid api key"), strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "authentication"), strings.Contains(msg, "please login"),
		strings.Contains(msg, "凭证过期"), strings.Contains(msg, "请重新登录"):
		f.Unauthorized = true
		f.ClientFixable = true
	}
}

// HTTPStatus 把失败分类映射为 HTTP 状态码。
func (f *Failure) HTTPStatus() int {
	if f == nil {
		return 500
	}
	switch {
	case f.Canceled:
		return 499 // 客户端断开
	case f.Timeout:
		return 504
	case f.Unauthorized:
		// 凭据错误必须是 401：客户端 SDK（Claude Code / openai-python 等）
		// 靠这个状态码区分「该换密钥了」与「请求体有问题」。
		return 401
	case f.ContextLength:
		return 413
	case f.RateLimited:
		return 429
	case f.ClientFixable:
		return 400
	case f.UpstreamFault:
		return 502
	default:
		return 502
	}
}

// ErrorType 把失败分类映射为 OpenAI 风格的错误类型。
func (f *Failure) ErrorType() string {
	if f == nil {
		return "server_error"
	}
	switch {
	case f.Canceled:
		return "server_error"
	case f.Timeout:
		return "server_error"
	case f.ContextLength:
		return "invalid_request_error"
	case f.RateLimited:
		return "rate_limit_error"
	case f.ClientFixable:
		return "invalid_request_error"
	default:
		return "server_error"
	}
}

// ErrorCode 返回 OpenAI 风格的机器可读错误码。
func (f *Failure) ErrorCode() string {
	if f == nil {
		return "internal_error"
	}
	switch {
	case f.ContextLength:
		return "context_length_exceeded"
	case f.RateLimited:
		return "rate_limit_exceeded"
	default:
		return f.Code
	}
}

// String 返回可读摘要，用于日志。
func (f *Failure) String() string {
	if f == nil {
		return "-"
	}
	return fmt.Sprintf("%s (http=%d type=%s)", f.Error(), f.HTTPStatus(), f.ErrorType())
}
