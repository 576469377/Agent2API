package workbuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"agent2api/internal/llm"
)

// 设备码登录流程（参考同类实现，本机未端到端验证——本机已有凭证无需登录）：
//  1. POST /v2/plugin/auth/state?platform=<platform>  取 authUrl + state
//  2. 浏览器打开 authUrl，用户完成登录
//  3. 轮询 GET /v2/plugin/auth/token?state=<state>    直到拿到 accessToken
//  4. 轮询 GET /v2/plugin/login/account?state=<state> 直到账户信息就绪
type loginState struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// LoginOptions 是登录参数。
type LoginOptions struct {
	// BaseURL 为上游地址，为空时用 DefaultBaseURL。
	BaseURL string

	// Platform 是上报给上游的客户端标识。
	Platform string
	// OutputPath 是凭证落盘路径，默认 ~/.workbuddy/session.json。
	OutputPath string
	// OnAuthURL 拿到授权 URL 时回调（用于打印或自动打开浏览器）。
	OnAuthURL func(url string)
	// Timeout 是等待用户在浏览器完成登录的总时长。
	Timeout time.Duration
}

// Login 执行设备码登录并把凭证写入磁盘。
//
// baseURL 为空时使用 DefaultBaseURL。
func Login(ctx context.Context, baseURL string, opts LoginOptions) (*Credential, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	cli := newHTTPClient(baseURL, 0)
	if opts.Platform == "" {
		opts.Platform = "VSCode"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}

	// 步骤 1：取授权 URL 与 state。
	headers := cli.authHeaders(nil, headerModeNoAuth)
	var resp loginState
	path := "/v2/plugin/auth/state?platform=" + urlQueryEscape(opts.Platform)
	if err := cli.doJSON(ctx, http.MethodPost, path, map[string]any{}, headers, &resp); err != nil {
		return nil, err
	}
	state, authURL, err := parseLoginState(resp.Data)
	if err != nil {
		return nil, err
	}
	if authURL == "" {
		return nil, &llm.Failure{Code: "login_no_url", Message: "上游响应缺少 authUrl", UpstreamFault: true}
	}
	if opts.OnAuthURL != nil {
		opts.OnAuthURL(authURL)
	}

	// 步骤 2+3：轮询换取 token。
	tok, err := pollForToken(ctx, cli, state, opts.Timeout)
	if err != nil {
		return nil, err
	}

	cred := &Credential{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		ExpiresAt:    tok.ExpiresAt,
		Domain:       tok.Domain,
		MachineID:    stableMachineID(),
	}
	if cred.TokenType == "" {
		cred.TokenType = "Bearer"
	}
	if cred.ExpiresAt == 0 && tok.ExpiresIn > 0 {
		cred.ExpiresAt = time.Now().UnixMilli() + tok.ExpiresIn*1000
	}

	// 步骤 4：账户信息可能异步就绪，需要重试。
	if acct, err := pollForAccount(ctx, cli, cred, state, 60*time.Second); err == nil {
		cred.UID = acct.UID
		cred.Nickname = acct.Nickname
		cred.EnterpriseID = acct.EnterpriseID
		cred.DepartmentInfo = acct.DepartmentInfo
	}

	if err := saveCredential(cred, opts.OutputPath); err != nil {
		return cred, err
	}
	return cred, nil
}

// parseLoginState 解析登录初始响应，取出 state 与 authUrl。
func parseLoginState(raw json.RawMessage) (state, authURL string, err error) {
	var m map[string]json.RawMessage
	if e := json.Unmarshal(raw, &m); e == nil {
		if inner, ok := m["data"]; ok {
			raw = inner
			var im map[string]json.RawMessage
			if e := json.Unmarshal(inner, &im); e == nil {
				if _, nested := im["data"]; nested {
					raw = im["data"]
				}
			}
		}
	}
	var payload struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
		URL     string `json:"url"`
	}
	if e := json.Unmarshal(raw, &payload); e != nil {
		return "", "", &llm.Failure{Code: "login_parse_failed", Message: "解析登录响应失败: " + e.Error(), Cause: e}
	}
	authURL = payload.AuthURL
	if authURL == "" {
		authURL = payload.URL
	}
	return payload.State, authURL, nil
}

// pollForToken 轮询换取 accessToken。
func pollForToken(ctx context.Context, cli *httpClient, state string, timeout time.Duration) (tokenPayload, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return tokenPayload{}, err
		}
		var resp loginState
		headers := cli.authHeaders(nil, headerModeNoAuth)
		path := "/v2/plugin/auth/token?state=" + urlQueryEscape(state)
		if err := cli.doJSON(ctx, http.MethodGet, path, nil, headers, &resp); err == nil {
			tok, err := unwrapToken(resp.Data)
			if err == nil && tok.AccessToken != "" {
				return tok, nil
			}
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	if lastErr != nil {
		return tokenPayload{}, lastErr
	}
	return tokenPayload{}, &llm.Failure{Code: "login_timeout", Message: "等待授权超时，请重试", ClientFixable: true}
}

// pollForAccount 轮询账户信息；上游在浏览器登录后需要异步准备账户数据。
func pollForAccount(ctx context.Context, cli *httpClient, cred *Credential, state string, timeout time.Duration) (accountInfo, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return accountInfo{}, err
		}
		headers := cli.authHeaders(cred, headerModeNoAuth)
		// /login/account 需要 Authorization 与 X-Domain，但不要 User/Enterprise 头。
		headers.Set("Authorization", cred.TokenType+" "+cred.AccessToken)
		headers.Del("X-No-Authorization")

		var resp loginState
		path := "/v2/plugin/login/account?state=" + urlQueryEscape(state)
		if err := cli.doJSON(ctx, http.MethodGet, path, nil, headers, &resp); err == nil {
			var acct accountInfo
			if e := json.Unmarshal(unwrapEnvelope(resp.Data), &acct); e == nil && acct.UID != "" {
				return acct, nil
			}
		}
		// 401 表示账户信息尚未就绪，继续轮询。
		time.Sleep(time.Second)
	}
	return accountInfo{}, &llm.Failure{Code: "account_timeout", Message: "账户信息获取超时", UpstreamFault: true}
}

type accountInfo struct {
	UID            string `json:"uid"`
	Nickname       string `json:"nickname"`
	EnterpriseID   string `json:"enterpriseId"`
	DepartmentInfo string `json:"departmentInfo"`
}

// unwrapEnvelope 解掉上游的 data 信封。
func unwrapEnvelope(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	inner, ok := m["data"]
	if !ok {
		return raw
	}
	var im map[string]json.RawMessage
	if err := json.Unmarshal(inner, &im); err == nil {
		if nested, has := im["data"]; has {
			return nested
		}
	}
	return inner
}

// saveCredential 把凭证写入磁盘（0600）。
func saveCredential(cred *Credential, path string) error {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, ".workbuddy", "session.json")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken":  cred.AccessToken,
			"refreshToken": cred.RefreshToken,
			"tokenType":    cred.TokenType,
			"expiresAt":    cred.ExpiresAt,
			"domain":       cred.Domain,
		},
		"account": map[string]any{
			"uid":            cred.UID,
			"nickname":       cred.Nickname,
			"enterpriseId":   cred.EnterpriseID,
			"departmentInfo": cred.DepartmentInfo,
		},
		"machineId": cred.MachineID,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return &llm.Failure{Code: "credential_write_failed", Message: "写入凭证失败: " + err.Error(), Cause: err}
	}
	fmt.Println("凭证已写入:", path)
	return nil
}

// urlQueryEscape 做最小可用的 query 转义。
func urlQueryEscape(s string) string {
	const upper = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'),
			c == '-' || c == '_' || c == '.' || c == '~':
			out = append(out, c)
		default:
			out = append(out, '%', upper[c>>4], upper[c&0xf])
		}
	}
	return string(out)
}
