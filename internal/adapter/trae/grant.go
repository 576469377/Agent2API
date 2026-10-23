package trae

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// 这是网页登录的设备绑定协议版本，不代表本机桌面安装版本。
const authProtocolVersion = "3.5.54"

type deviceInfo struct {
	DeviceID        string `json:"DeviceID"`
	MachineID       string `json:"MachineID"`
	PlatformCode    string `json:"PlatformCode"`
	DeviceType      string `json:"DeviceType"`
	DeviceName      string `json:"DeviceName"`
	DeviceModel     string `json:"DeviceModel"`
	ClientVersion   string `json:"ClientVersion"`
	DevicePublicKey string `json:"DevicePublicKey"`
	DeviceBrand     string `json:"DeviceBrand"`
	OSInfo          string `json:"OSInfo"`
	OSVersion       string `json:"OSVersion"`
}

type authorizationGrant struct {
	Code       string     `json:"code"`
	Verifier   string     `json:"verifier"`
	Device     deviceInfo `json:"device"`
	PrivateKey string     `json:"private_key"`
	ExpiresAt  time.Time  `json:"expires_at"`
}

func newAuthorizationGrant(code, verifier string, d loginDevice) (authorizationGrant, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return authorizationGrant{}, errors.New("无法创建 TRAE 授权设备密钥")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return authorizationGrant{}, errors.New("无法编码 TRAE 授权设备密钥")
	}
	defer clear(privateDER)
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return authorizationGrant{}, errors.New("无法编码 TRAE 授权设备公钥")
	}
	osInfo, brand := runtime.GOOS, runtime.GOOS
	if osInfo == "darwin" {
		osInfo, brand = "mac", "Apple"
	}
	if osInfo == "windows" {
		brand = "Microsoft"
	}
	return authorizationGrant{Code: code, Verifier: verifier, PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})), ExpiresAt: time.Now().Add(10 * time.Minute),
		Device: deviceInfo{DeviceID: d.DeviceID, MachineID: d.MachineID, PlatformCode: "IDE_PC", DeviceType: "PC", DeviceName: "Agent2API", DeviceModel: "PC", ClientVersion: authProtocolVersion, DevicePublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})), DeviceBrand: brand, OSInfo: osInfo, OSVersion: osInfo}}, nil
}

func grantPath(path string) string {
	if path == "" {
		path = DefaultCredentialPath()
	}
	return path + ".oauth.session.json"
}

func saveGrant(path string, g authorizationGrant) error {
	path = grantPath(path)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return errors.New("无法保存 TRAE 待兑换授权")
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return errors.New("无法编码 TRAE 待兑换授权")
	}
	defer clear(raw)
	f, err := os.CreateTemp(filepath.Dir(path), ".trae-oauth-*.session.json.tmp")
	if err != nil {
		return errors.New("无法创建 TRAE 授权临时文件")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil || os.Rename(f.Name(), path) != nil {
		return errors.New("无法写入 TRAE 待兑换授权")
	}
	return nil
}

func finishAuthorization(ctx context.Context, client *http.Client, base, path string, g authorizationGrant) (*Credential, error) {
	c, err := exchangeToken(ctx, client, base, g.Code, g.Verifier, "", g)
	if c.Token != "" {
		c.Pending = true
		if saveErr := saveCredential(pendingLoginPath(path), c); saveErr != nil {
			return nil, errors.New("TRAE 兑换结果保存失败，待兑换授权已保留")
		}
		_ = os.Remove(grantPath(path))
	}
	if err != nil {
		return nil, err
	}
	c.Pending = false
	if err := saveCredential(path, c); err != nil {
		return nil, err
	}
	_ = os.Remove(pendingLoginPath(path))
	return &c, nil
}
