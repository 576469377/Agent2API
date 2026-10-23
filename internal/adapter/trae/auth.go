// Package trae 复用 TRAE 国内版账号的免费奖励积分。
package trae

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

const Platform = "trae-cn"
const authBaseURL = "https://api.trae.cn"
const publicClientID = "ono9krqynydwx5"

type Credential struct {
	Token            string      `json:"token"`
	RefreshToken     string      `json:"refreshToken,omitempty"`
	UserID           string      `json:"userId"`
	ExpiresAt        string      `json:"expiredAt"`
	RefreshExpiresAt string      `json:"refreshExpiredAt,omitempty"`
	ClientID         string      `json:"clientId"`
	Region           string      `json:"region"`
	Pending          bool        `json:"pending,omitempty"`
	DevicePrivateKey string      `json:"device_private_key,omitempty"`
	Device           *deviceInfo `json:"device,omitempty"`
}

func DefaultCredentialPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent2api", Platform+".session.json")
}

func loginRequired() *llm.Failure {
	return &llm.Failure{Code: "trae_login_required", Message: "请通过 TRAE 国内版网页授权登录，再刷新模型目录", Unauthorized: true}
}

func expiryTime(value string) time.Time {
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n > 1e12 {
			return time.UnixMilli(n)
		}
		return time.Unix(n, 0)
	}
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

func readCredential(path string) (Credential, error) {
	if path == "" {
		path = DefaultCredentialPath()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credential{}, loginRequired()
	}
	defer clear(raw)
	var c Credential
	if len(raw) > 1024*1024 || json.Unmarshal(raw, &c) != nil || c.Pending || c.ClientID != publicClientID || c.Region != "CN" || c.UserID == "" || c.Token == "" || expiryTime(c.ExpiresAt).IsZero() {
		return Credential{}, loginRequired()
	}
	return c, nil
}

// 新文件先完整写好再替换；不修改桌面客户端的登录文件。
func saveCredential(path string, c Credential) error {
	if path == "" {
		path = DefaultCredentialPath()
	}
	if old, err := readCredential(path); err == nil && old.UserID != c.UserID {
		return errors.New("TRAE 凭据文件已属于另一个账号，请使用新的凭据路径")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("无法创建 TRAE 凭据目录")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return errors.New("无法编码 TRAE 凭据")
	}
	defer clear(raw)
	f, err := os.CreateTemp(filepath.Dir(path), ".trae-*.session.json.tmp")
	if err != nil {
		return errors.New("无法创建 TRAE 凭据临时文件")
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return errors.New("无法写入 TRAE 凭据")
	}
	if err = f.Close(); err != nil {
		return errors.New("无法保存 TRAE 凭据")
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.New("无法替换 TRAE 凭据文件")
	}
	return nil
}

type exchangeEnvelope struct {
	Result struct {
		Token         string          `json:"Token"`
		AccessToken   string          `json:"AccessToken"`
		RefreshToken  string          `json:"RefreshToken"`
		TokenExpiry   json.RawMessage `json:"TokenExpireAt"`
		ExpiresAt     json.RawMessage `json:"ExpiresAt"`
		RefreshExpiry json.RawMessage `json:"RefreshExpireAt"`
		UserID        string          `json:"UserID"`
		UserInfo      json.RawMessage `json:"UserInfo"`
	} `json:"Result"`
	ResponseMetadata struct {
		Error *struct {
			Code json.RawMessage `json:"Code"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
}

func scalarString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	if len(raw) > 0 && raw[0] >= '0' && raw[0] <= '9' {
		return string(raw)
	}
	return ""
}

func exchangeToken(ctx context.Context, client *http.Client, base, code, verifier, refresh string, grants ...authorizationGrant) (Credential, error) {
	body := map[string]any{"ClientID": publicClientID, "ClientSecret": "-", "UserID": ""}
	path := "/cloudide/api/v3/trae/oauth/ExchangeToken"
	var grant authorizationGrant
	if code != "" {
		if len(grants) != 1 || grants[0].PrivateKey == "" || grants[0].Device.DeviceID == "" || grants[0].Device.MachineID == "" {
			return Credential{}, errors.New("TRAE 授权缺少本机设备绑定信息")
		}
		grant = grants[0]
		path = "/trae/api/v3/oauth/ExchangeToken"
		body = map[string]any{"ClientID": publicClientID, "AuthCode": code, "CodeVerifier": verifier, "DeviceInfo": grant.Device, "IDEVersion": authProtocolVersion}
	} else {
		body["RefreshToken"] = refresh
	}
	raw, _ := json.Marshal(body)
	defer clear(raw)
	req, _ := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Credential{}, &llm.Failure{Code: "trae_auth_network", Message: "连接 TRAE 授权服务失败", Cause: err}
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return Credential{}, &llm.Failure{Code: "trae_auth_invalid_response", Message: "TRAE 授权服务返回的数据不可读", UpstreamFault: true}
	}
	defer clear(raw)
	var data exchangeEnvelope
	if json.Unmarshal(raw, &data) != nil {
		return Credential{}, &llm.Failure{Code: "trae_auth_invalid_json", Message: "TRAE 授权服务返回的数据格式无效", UpstreamFault: true}
	}
	if resp.StatusCode != 200 || data.ResponseMetadata.Error != nil {
		code := "unknown"
		if data.ResponseMetadata.Error != nil {
			code = safeAuthCode(scalarString(data.ResponseMetadata.Error.Code))
		}
		return Credential{}, &llm.Failure{Code: "trae_auth_exchange_" + strconv.Itoa(resp.StatusCode) + "_" + code, Message: "TRAE 拒绝了凭据兑换，请检查授权参数", ClientFixable: true}
	}
	r := data.Result
	info := r.UserInfo
	if text := scalarString(info); text != "" {
		info = []byte(text)
	}
	var user struct {
		ID     string `json:"UserID"`
		Region string `json:"Region"`
	}
	_ = json.Unmarshal(info, &user)
	if user.ID == "" {
		user.ID = r.UserID
	}
	// 账号身份与到期时刻都必须来自兑换服务，不能信任浏览器回调自报的身份。
	c := Credential{Token: r.Token, RefreshToken: r.RefreshToken, UserID: user.ID, ExpiresAt: scalarString(r.TokenExpiry), RefreshExpiresAt: scalarString(r.RefreshExpiry), ClientID: publicClientID, Region: strings.ToUpper(user.Region)}
	if code != "" {
		c.Device = &grant.Device
		c.DevicePrivateKey = grant.PrivateKey
	}
	if c.ExpiresAt == "" {
		c.ExpiresAt = scalarString(r.ExpiresAt)
	}
	if c.Token == "" {
		c.Token = r.AccessToken
	}
	if c.ExpiresAt == "" {
		// 此 token 直接来自固定的 HTTPS 兑换服务；不从浏览器提交的 token 取信任。
		parts := strings.Split(c.Token, ".")
		if len(parts) == 3 {
			if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
				var claims struct {
					Exp int64 `json:"exp"`
				}
				if json.Unmarshal(payload, &claims) == nil && claims.Exp > 0 {
					c.ExpiresAt = strconv.FormatInt(claims.Exp, 10)
				}
				clear(payload)
			}
		}
	}
	if c.Region == "" {
		c.Region = "CN"
	}
	c.Region = normalizeRegion(c.Region)
	if c.Token == "" {
		return c, &llm.Failure{Code: "trae_auth_token_missing", Message: "TRAE 兑换响应未包含访问凭据", UpstreamFault: true}
	}
	if !expiryTime(c.ExpiresAt).After(time.Now()) {
		return c, &llm.Failure{Code: "trae_auth_expiry_invalid", Message: "TRAE 兑换凭据的有效期缺失或已过期", Unauthorized: true}
	}
	if c.RefreshToken == "" {
		c.RefreshToken = refresh
	}
	if c.UserID == "" {
		if err := resolveCredentialIdentity(ctx, client, base, &c); err != nil {
			return c, err
		}
	}
	if c.Region != "CN" {
		return c, &llm.Failure{Code: "trae_auth_region", Message: "当前授权不属于 TRAE 国内版", ClientFixable: true}
	}
	return c, nil
}

func normalizeRegion(value string) string {
	switch strings.ToLower(value) {
	case "cn", "china", "china-north":
		return "CN"
	default:
		return strings.ToUpper(value)
	}
}

func safeAuthCode(value string) string {
	if value == "" || len(value) > 64 {
		return "unknown"
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return "unknown"
		}
	}
	return value
}

func resolveCredentialIdentity(ctx context.Context, client *http.Client, base string, c *Credential) error {
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/cloudide/api/v3/trae/GetUserInfo", strings.NewReader(`{"ReqSource":"IDE"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-cloudide-token", c.Token)
	resp, err := client.Do(req)
	if err != nil {
		return &llm.Failure{Code: "trae_auth_profile_network", Message: "TRAE 凭据已兑换，但账号信息暂时无法读取", Cause: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return &llm.Failure{Code: "trae_auth_profile_read", Message: "TRAE 账号信息不可读", UpstreamFault: true}
	}
	defer clear(raw)
	var profile struct {
		Result struct {
			UserID string `json:"UserID"`
			Region string `json:"Region"`
		} `json:"Result"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(raw, &profile) != nil || profile.Result.UserID == "" {
		return &llm.Failure{Code: "trae_auth_profile_" + strconv.Itoa(resp.StatusCode), Message: "TRAE 凭据已兑换，但账号身份尚未确认", UpstreamFault: true}
	}
	c.UserID = profile.Result.UserID
	if profile.Result.Region != "" {
		c.Region = normalizeRegion(profile.Result.Region)
	}
	return nil
}
