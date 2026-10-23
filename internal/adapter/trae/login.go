package trae

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

type LoginOptions struct {
	OutputPath string
	OnAuthURL  func(string)
	device     loginDevice
}

// 网页授权使用官方 S256 PKCE。回调固定为官网校验的 /authorize，
// 并核对官网返回的 loginTraceID；不接受回调携带的 host 或直接提交的 token。
func Login(ctx context.Context, opts LoginOptions) (*Credential, error) {
	if c, err := resumeLogin(ctx, opts.OutputPath); c != nil || err != nil {
		return c, err
	}
	d, err := loadLoginDevice()
	if err != nil {
		return nil, err
	}
	opts.device = d
	return login(ctx, opts, authBaseURL, &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
}

func login(ctx context.Context, opts LoginOptions, base string, client *http.Client) (*Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	var entropy [64]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, fmt.Errorf("无法初始化 TRAE 网页授权")
	}
	state := base64.RawURLEncoding.EncodeToString(entropy[:32])
	verifier := base64.RawURLEncoding.EncodeToString(entropy[32:])
	challenge := sha256.Sum256([]byte(verifier))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("无法启动 TRAE 本机授权回调")
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	query := url.Values{
		"client_id": {publicClientID}, "auth_from": {"trae"}, "auth_type": {"local"},
		"login_channel": {"native_ide"}, "login_version": {"1"}, "redirect": {"0"},
		"auth_callback_url": {origin + "/authorize"}, "login_trace_id": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
		"device_id": {opts.device.DeviceID}, "machine_id": {opts.device.MachineID},
	}
	urlToOpen := "https://www.trae.cn/authorization?" + query.Encode()
	type result struct {
		cred *Credential
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Host != listener.Addr().String() || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "授权链接无效", http.StatusBadRequest)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, urlToOpen, http.StatusSeeOther)
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		if r.Method != "GET" || r.Host != listener.Addr().String() || len(r.RequestURI) > 65536 || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("loginTraceID")), []byte(state)) != 1 {
			http.Error(w, "授权回调不匹配，请重新发起登录", http.StatusBadRequest)
			return
		}
		handled := false
		once.Do(func() {
			handled = true
			var info struct {
				AuthCode string `json:"AuthCode"`
			}
			if json.Unmarshal([]byte(r.URL.Query().Get("authCodeInfo")), &info) != nil || info.AuthCode == "" {
				http.Error(w, "未收到有效授权码，请重新发起登录", http.StatusBadRequest)
				done <- result{err: fmt.Errorf("TRAE 网页未返回有效授权码")}
				return
			}
			grant, err := newAuthorizationGrant(info.AuthCode, verifier, opts.device)
			if err == nil {
				err = saveGrant(opts.OutputPath, grant)
			}
			var cred *Credential
			if err == nil {
				cred, err = finishAuthorization(ctx, client, base, opts.OutputPath, grant)
			}
			if err != nil {
				http.Error(w, "授权保存失败，请返回 Agent2API 查看状态", http.StatusBadGateway)
				done <- result{err: err}
				return
			}
			_ = os.Remove(pendingLoginPath(opts.OutputPath))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>TRAE 授权完成</title><script>history.replaceState(null,'','/authorize')</script><style>body{font:18px system-ui;max-width:36em;margin:15vh auto;padding:2em;line-height:1.7}</style><h1>TRAE 授权完成</h1><p>登录结果已保存在本机。可以关闭此页，返回 Agent2API。</p>")
			done <- result{cred: cred}
		})
		if !handled {
			http.Error(w, "授权已处理，请返回 Agent2API", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 64 * 1024, ErrorLog: log.New(io.Discard, "", 0)}
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if server.Shutdown(shutdownCtx) != nil {
			server.Close()
		}
	}()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			select {
			case done <- result{err: fmt.Errorf("TRAE 本机授权监听已停止")}:
			default:
			}
		}
	}()
	if opts.OnAuthURL != nil {
		opts.OnAuthURL(origin + "/login?state=" + url.QueryEscape(state))
	}
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("TRAE 网页授权已取消或超时，请重新发起登录")
	case r := <-done:
		return r.cred, r.err
	}
}

func pendingLoginPath(path string) string {
	if path == "" {
		path = DefaultCredentialPath()
	}
	return path + ".login.tmp"
}

// 身份查询或落盘失败时重用已经兑换的本机临时凭据，避免再次要求用户网页登录。
func resumeLogin(ctx context.Context, path string) (*Credential, error) {
	raw, err := os.ReadFile(pendingLoginPath(path))
	if os.IsNotExist(err) {
		raw, err = os.ReadFile(grantPath(path))
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("无法读取 TRAE 待兑换授权")
		}
		defer clear(raw)
		var grant authorizationGrant
		if len(raw) > 1024*1024 || json.Unmarshal(raw, &grant) != nil || !grant.ExpiresAt.After(time.Now()) || grant.Code == "" || grant.Verifier == "" || grant.PrivateKey == "" {
			return nil, fmt.Errorf("TRAE 待兑换授权已过期或不可读，请使用新的 -out 路径重新授权")
		}
		client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		return finishAuthorization(ctx, client, authBaseURL, path, grant)
	}
	if err != nil {
		return nil, fmt.Errorf("无法读取 TRAE 待恢复的授权结果")
	}
	defer clear(raw)
	var c Credential
	if len(raw) > 1024*1024 || json.Unmarshal(raw, &c) != nil || c.ClientID != publicClientID || !c.Pending || c.Token == "" || !expiryTime(c.ExpiresAt).After(time.Now()) {
		return nil, fmt.Errorf("TRAE 待恢复授权已过期或不可读，请保留临时文件并使用新的 -out 路径重新授权")
	}
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if err := resolveCredentialIdentity(ctx, client, authBaseURL, &c); err != nil {
		return nil, err
	}
	if c.Region != "CN" {
		return nil, fmt.Errorf("TRAE 待恢复授权不属于国内版")
	}
	c.Pending = false
	if err := saveCredential(path, c); err != nil {
		return nil, err
	}
	_ = os.Remove(pendingLoginPath(path))
	return &c, nil
}
