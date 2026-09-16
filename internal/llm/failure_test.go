package llm

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
)

// ───────────────────────── classify 派生分类 ─────────────────────────

func TestClassifyContextCanceled(t *testing.T) {
	f := NewFailure("x", " canceled", context.Canceled)
	if !f.Canceled || !f.ClientFixable {
		t.Fatalf("context.Canceled 应派生 Canceled+ClientFixable, got %+v", f)
	}
	if got := f.HTTPStatus(); got != 499 {
		t.Fatalf("Canceled 的 HTTPStatus=%d, want 499", got)
	}
}

func TestClassifyTimeout(t *testing.T) {
	// 构造一个真的实现了 net.Error 的超时错误。
	timerErr := &timeoutError{}
	f := NewFailure("x", "dial upstream", timerErr)
	if !f.Timeout {
		t.Fatalf("net.Error.Timeout 应派生 Timeout, got %+v", f)
	}
	if got := f.HTTPStatus(); got != 504 {
		t.Fatalf("Timeout 的 HTTPStatus=%d, want 504", got)
	}
}

type timeoutError struct{}

func (e *timeoutError) Error() string   { return "i/o timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

// 另一种：通过 url.Error 包装的常见超时。
func TestClassifyTimeoutViaText(t *testing.T) {
	f := NewFailure("x", "request timeout while reading", nil)
	if !f.Timeout {
		t.Fatalf("文本 timeout 应派生 Timeout, got %+v", f)
	}
}

func TestClassifyRateLimited(t *testing.T) {
	// 注意刻意**不含**裸数字 "429"：见 TestClassifyDoesNotMisfireOnEmbeddedLiterals。
	for _, msg := range []string{"Too Many Requests", "rate limit exceeded", "quota exhausted", "rate limited by upstream"} {
		f := NewFailure("x", msg, nil)
		if !f.RateLimited {
			t.Errorf("%q 应派生 RateLimited, got %+v", msg, f)
		}
		if got := f.HTTPStatus(); got != 429 {
			t.Errorf("%q 的 HTTPStatus=%d, want 429", msg, got)
		}
	}
}

func TestClassifyContextLength(t *testing.T) {
	f := NewFailure("x", "this model's maximum context length is 32768 tokens", nil)
	if !f.ContextLength || !f.ClientFixable {
		t.Fatalf("context length 应派生 ContextLength+ClientFixable, got %+v", f)
	}
	if got := f.HTTPStatus(); got != 413 {
		t.Fatalf("ContextLength 的 HTTPStatus=%d, want 413", got)
	}
	if got := f.ErrorCode(); got != "context_length_exceeded" {
		t.Fatalf("ErrorCode=%q, want context_length_exceeded", got)
	}
}

func TestClassifyConnReset(t *testing.T) {
	f := NewFailure("upstream_read_failed", "read: connection reset by peer", syscall.ECONNRESET)
	if !f.UpstreamFault {
		t.Fatalf("ECONNRESET 应派生 UpstreamFault, got %+v", f)
	}
	if got := f.HTTPStatus(); got != 502 {
		t.Fatalf("UpstreamFault 的 HTTPStatus=%d, want 502", got)
	}
}

func TestClassifyUnauthorized(t *testing.T) {
	for _, msg := range []string{"invalid api key", "401 unauthorized", "authentication required"} {
		f := NewFailure("x", msg, nil)
		if !f.Unauthorized || !f.ClientFixable {
			t.Errorf("%q 应派生 Unauthorized+ClientFixable, got %+v", msg, f)
		}
		if got := f.HTTPStatus(); got != 401 {
			t.Errorf("%q 的 HTTPStatus=%d, want 401（客户端 SDK 靠 401 触发重新配置凭据）", msg, got)
		}
	}
}

func TestClassifyPriorityCanceledBeatsRest(t *testing.T) {
	// 取消优先于一切：取消的请求不应再被误判为超时。
	f := NewFailure("x", "timeout while canceled", context.Canceled)
	if !f.Canceled {
		t.Fatalf("Canceled 应优先, got %+v", f)
	}
	if f.Timeout {
		t.Fatalf("取消的请求不应再派生 Timeout, got %+v", f)
	}
}

// ───────────────────────── Wrap 语义 ─────────────────────────

// TestWrapTypedFailureReclassifies 是回归测试：
// 调用方直接构造 Failure 字面量（只填生产侧字段）再交给 Wrap 时，
// 派生字段必须被补齐。曾出现超时被误判为通用上游故障（502 而非 504）。
func TestWrapTypedFailureReclassifies(t *testing.T) {
	typed := &Failure{Code: "upstream_unreachable", Message: "dial: i/o timeout", Cause: &timeoutError{}}
	f := Wrap(typed)
	if f != typed {
		t.Fatalf("Wrap 应返回同一个 *Failure（保持 errors.As 链）")
	}
	if !f.Timeout {
		t.Fatalf("Wrap 必须为字面量补齐派生字段: Timeout 应为 true, got %+v", f)
	}
	if got := f.HTTPStatus(); got != 504 {
		t.Fatalf("HTTPStatus=%d, want 504", got)
	}
}

func TestWrapPlainError(t *testing.T) {
	plain := errors.New("something broke")
	f := Wrap(plain)
	if f == nil || f.Code != "internal_error" {
		t.Fatalf("普通 error 应被包成 internal_error, got %+v", f)
	}
	if !errors.Is(f, plain) {
		t.Fatal("Wrap 应保留 errors.Is 链")
	}
}

func TestWrapNil(t *testing.T) {
	if f := Wrap(nil); f != nil {
		t.Fatalf("Wrap(nil) 应返回 nil, got %+v", f)
	}
}

func TestWrapIdempotentClassification(t *testing.T) {
	// 显式分类不应被 Wrap 的重分类破坏。
	f := &Failure{Code: "rate_limited", Message: "slow down", RateLimited: true, ClientFixable: true}
	g := Wrap(f)
	if !g.RateLimited || !g.ClientFixable {
		t.Fatalf("重分类应幂等, got %+v", g)
	}
}

// ───────────────────────── HTTPStatus / ErrorType 全表 ─────────────────────────

func TestHTTPStatusTable(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Failure)
		want int
	}{
		{"nil", nil, 500},
		{"empty", func(f *Failure) {}, 502},
		{"canceled", func(f *Failure) { f.Canceled = true }, 499},
		{"timeout", func(f *Failure) { f.Timeout = true }, 504},
		{"unauthorized", func(f *Failure) { f.Unauthorized = true }, 401},
		{"context_length", func(f *Failure) { f.ContextLength = true }, 413},
		{"rate_limited", func(f *Failure) { f.RateLimited = true }, 429},
		{"client_fixable", func(f *Failure) { f.ClientFixable = true }, 400},
		{"upstream_fault", func(f *Failure) { f.UpstreamFault = true }, 502},
	}
	for _, c := range cases {
		var f *Failure
		if c.mut != nil {
			f = &Failure{}
			c.mut(f)
		}
		if got := f.HTTPStatus(); got != c.want {
			t.Errorf("%s: HTTPStatus=%d, want %d", c.name, got, c.want)
		}
	}
}

func TestErrorTypeTable(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Failure)
		want string
	}{
		{"context_length", func(f *Failure) { f.ContextLength = true }, "invalid_request_error"},
		{"rate_limited", func(f *Failure) { f.RateLimited = true }, "rate_limit_error"},
		{"client_fixable", func(f *Failure) { f.ClientFixable = true }, "invalid_request_error"},
		{"timeout", func(f *Failure) { f.Timeout = true }, "server_error"},
		{"empty", func(f *Failure) {}, "server_error"},
	}
	for _, c := range cases {
		f := &Failure{}
		c.mut(f)
		if got := f.ErrorType(); got != c.want {
			t.Errorf("%s: ErrorType=%q, want %q", c.name, got, c.want)
		}
	}
}

// TestFailureErrorFormat 保证日志可检索的 "<code>: <msg>" 格式。
func TestFailureErrorFormat(t *testing.T) {
	f := NewFailure("11101", "非流式不支持", nil)
	if got := f.Error(); got != "11101: 非流式不支持" {
		t.Fatalf("Error()=%q", got)
	}
	if got := f.String(); got == "" || !strings.Contains(got, "11101") {
		t.Fatalf("String()=%q", got)
	}
	var e error = f
	if got := e.Error(); got != "11101: 非流式不支持" {
		t.Fatalf("error 接口 Error()=%q", got)
	}
}

// TestClassifyDoesNotMisfireOnEmbeddedLiterals 保证文本匹配不被消息里的
// 偶然字面量触发：错误消息回显请求体片段（含 "401"/"429"）或包含
// "user_401" 这类 ID 时，不得把 decode_failed 误判成鉴权/限流失败。
func TestClassifyDoesNotMisfireOnEmbeddedLiterals(t *testing.T) {
	f := Wrap(&Failure{Code: "decode_failed", Message: `解析请求体失败: invalid character near "user_401"`})
	if f.Unauthorized || f.HTTPStatus() == 401 {
		t.Fatalf("decode_failed 不应因消息含 401 被判成鉴权失败: %+v http=%d", f, f.HTTPStatus())
	}

	f2 := Wrap(&Failure{Code: "encode_failed", Message: `序列化失败: {"retries": 429}`})
	if f2.RateLimited || f2.HTTPStatus() == 429 {
		t.Fatalf("encode_failed 不应因消息含 429 被判成限流: %+v http=%d", f2, f2.HTTPStatus())
	}

	// 反向锚点：generic Code 仍靠文本推断（守卫不能误伤兜底路径）。
	f3 := NewFailure("http_500", "upstream said: too many requests", nil)
	if !f3.RateLimited {
		t.Fatalf("generic Code 的文本兜底不应失效: %+v", f3)
	}
}
