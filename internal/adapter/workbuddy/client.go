package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"agent2api/internal/llm"
)

// 上游端点常量。
const (
	// DefaultBaseURL 由 /v3/config 的 endpoint 字段确认。
	DefaultBaseURL = "https://copilot.tencent.com"

	chatCompletionsPath = "/v2/chat/completions"
	configPath          = "/v3/config"
	tokenRefreshPath    = "/v2/plugin/auth/token/refresh"

	// 插件版本相关头，上游用于识别客户端。
	productCode     = "codebuddy"
	productVersion  = "4.10.33259736"
	ideType         = "vscode"
	ideName         = "Visual Studio Code"
	ideVersion      = "1.70.2"
	chatUserAgent   = "Mozilla/5.0 (compatible; Genie-IDE/1.0)"
	configUserAgent = "CodeBuddyIDE/" + productVersion
)

// headerMode 决定请求头组合。
type headerMode int

const (
	headerModeFull    headerMode = iota // 带 Authorization
	headerModeRefresh                   // 不带 Authorization，带 X-Refresh-Token
	headerModeNoAuth                    // 登录阶段，显式声明「没有」这些头
	headerModeConfig                    // /v3/config 专用 UA（上游校验 UA，否则 12403）
)

// httpClient 封装上游 HTTP 调用。
type httpClient struct {
	baseURL string
	http    *http.Client
}

func newHTTPClient(baseURL string, timeout time.Duration) *httpClient {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &httpClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		// 不设 Client.Timeout：它会作用于整个响应体读取，必然误杀长流。
		// 超时改由 ctx deadline 控制（非流式）+ 空闲看门狗控制（流式）。
		http: &http.Client{},
	}
}

// authHeaders 构造上游要求的请求头。
//
// 实测确认的最小集合：Authorization + 一组 X-IDE-* / X-Product-* 身份头。
// 上游并不校验 X-Machine-Id 的具体值，但要求稳定。
func (c *httpClient) authHeaders(cred *Credential, mode headerMode) http.Header {
	h := http.Header{}
	h.Set("X-Product-Code", productCode)
	h.Set("X-Product-Version", productVersion)
	h.Set("Accept", "application/json")

	switch mode {
	case headerModeConfig:
		// 上游配置服务校验 UA，generic UA 会被拒（code 12403 "check ua"）。
		// 注意此处 X-IDE-Type/Name 用大写的 VSCode，与聊天请求不同。
		h.Set("User-Agent", configUserAgent)
		h.Set("X-IDE-Type", "VSCode")
		h.Set("X-IDE-Name", "VSCode")
		h.Set("X-IDE-Version", ideVersion)
		h.Set("X-Product", "SaaS")
		h.Set("X-Requested-With", "XMLHttpRequest")
	default:
		h.Set("User-Agent", chatUserAgent)
		h.Set("X-IDE-Type", ideType)
		h.Set("X-IDE-Name", ideName)
		h.Set("X-IDE-Version", ideVersion)
	}

	if cred == nil {
		return h
	}

	if cred.MachineID != "" {
		h.Set("X-Machine-Id", cred.MachineID)
	}
	if cred.Domain != "" {
		h.Set("X-Domain", cred.Domain)
	}

	switch mode {
	case headerModeNoAuth:
		h.Set("X-No-Authorization", "true")
		h.Set("X-No-User-Id", "true")
		h.Set("X-No-Enterprise-Id", "true")
		h.Set("X-No-Department-Info", "true")
	case headerModeRefresh:
		h.Set("X-Refresh-Token", cred.RefreshToken)
	default:
		if cred.AccessToken != "" {
			h.Set("Authorization", cred.TokenType+" "+cred.AccessToken)
		}
		if cred.UID != "" {
			h.Set("X-User-Id", cred.UID)
		}
		if cred.EnterpriseID != "" {
			h.Set("X-Enterprise-Id", cred.EnterpriseID)
			h.Set("X-Tenant-Id", cred.EnterpriseID)
		}
		if cred.DepartmentInfo != "" {
			h.Set("X-Department-Info", cred.DepartmentInfo)
		}
	}
	return h
}

// envelope 是上游统一响应信封。
type envelope struct {
	Code        int             `json:"code"`
	Msg         string          `json:"msg"`
	RequestID   string          `json:"requestId"`
	Data        json.RawMessage `json:"data"`
	DisplayMsg  json.RawMessage `json:"displayMsg"`
	Error       string          `json:"error"`
	Description string          `json:"error_description"`
}

// doJSON 发起请求并把响应解析为 out（out 可为 *envelope 或任意结构）。
func (c *httpClient) doJSON(ctx context.Context, method, path string, body any, headers http.Header, out any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return &llm.Failure{Code: "encode_failed", Message: "序列化请求体失败", Cause: err}
		}
		payload = bytes.NewReader(raw)
		if headers.Get("Content-Type") == "" {
			headers.Set("Content-Type", "application/json")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return &llm.Failure{Code: "request_build_failed", Message: err.Error(), Cause: err}
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return llm.Wrap(&llm.Failure{Code: "upstream_unreachable", Message: "请求上游失败: " + err.Error(), Cause: err, UpstreamFault: true})
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return llm.Wrap(&llm.Failure{Code: "upstream_read_failed", Message: "读取上游响应失败", Cause: err, UpstreamFault: true})
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpError(resp.StatusCode, raw, resp.Header.Get("Retry-After"))
	}
	decoded := decodeBytes(raw)
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(decoded, out); err != nil {
		return &llm.Failure{Code: "decode_failed", Message: "解析上游响应失败: " + err.Error(), Cause: err, UpstreamFault: true}
	}
	// 上游 HTTP 200 也可能带业务错误码
	if env, ok := out.(*envelope); ok && env.Code != 0 {
		return &llm.Failure{Code: fmt.Sprintf("upstream_%d", env.Code), Message: env.Msg, UpstreamFault: env.Code >= 500}
	}
	return nil
}

// httpError 把上游 HTTP 错误映射为 Failure。
func httpError(status int, raw []byte, retryAfter string) error {
	msg := strings.TrimSpace(string(raw))
	var env envelope
	if err := json.Unmarshal(decodeBytes(raw), &env); err == nil {
		if env.Msg != "" {
			msg = env.Msg
		} else if env.Error != "" {
			msg = env.Error
			if env.Description != "" {
				msg += ": " + env.Description
			}
		}
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	f := &llm.Failure{
		Code:          fmt.Sprintf("http_%d", status),
		Message:       msg,
		ClientFixable: status == 400 || status == 401 || status == 403 || status == 404 || status == 422,
		UpstreamFault: status >= 500,
	}
	if status == 401 {
		f.Code = "unauthorized"
		f.ClientFixable = true
	}
	if status == 429 {
		f.Code = "rate_limited"
	}
	if retryAfter != "" {
		var secs int
		if _, err := fmt.Sscanf(retryAfter, "%d", &secs); err == nil {
			f.RetryAfterSeconds = secs
		}
	}
	f.Classify()
	return f
}

// decodeBytes 解码上游字节。上游偶发返回非 UTF-8（如 GBK），按 utf8 → GBK → GB18030 → 替换 兜底。
func decodeBytes(raw []byte) []byte {
	if utf8.Valid(raw) {
		return raw
	}
	if out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw); err == nil {
		return out
	}
	if out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), raw); err == nil {
		return out
	}
	return []byte(strings.ToValidUTF8(string(raw), "�"))
}

// upstreamStream 发起流式请求，返回响应体供调用方逐行扫描。
// 调用方负责关闭。
func (c *httpClient) openStream(ctx context.Context, path string, body any, headers http.Header) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, &llm.Failure{Code: "encode_failed", Message: "序列化请求体失败", Cause: err}
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, &llm.Failure{Code: "request_build_failed", Message: err.Error(), Cause: err}
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, llm.Wrap(&llm.Failure{Code: "upstream_unreachable", Message: "连接上游失败: " + err.Error(), Cause: err, UpstreamFault: true})
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, httpError(resp.StatusCode, raw, resp.Header.Get("Retry-After"))
	}
	return resp, nil
}

// isRetryableTransportError 判断是否为可重试的传输错误（不含语义拒绝）。
func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	var f *llm.Failure
	if errors.As(err, &f) {
		if f.Code == "upstream_unreachable" || f.Code == "upstream_read_failed" {
			return true
		}
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}
