package app

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/zcode"
)

func localClient(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return false
	}
	// 独立控制台会代理到本机；不能因此把远程访问者误认成本机用户。
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		for _, address := range strings.Split(forwarded, ",") {
			if !net.ParseIP(strings.TrimSpace(address)).IsLoopback() {
				return false
			}
		}
	}
	return true
}

func (a *App) zcodeConfigPath() string {
	if a.zcodePath != "" {
		return a.zcodePath
	}
	return zcode.DefaultPath()
}

func (a *App) localGatewayOrigin() string {
	host := a.cfg.Server.Host
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	host = strings.Trim(host, "[]")
	return "http://" + net.JoinHostPort(host, strconv.Itoa(a.cfg.Server.Port))
}

func (a *App) apiZCode(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, message string) {
		writeJSONWithStatus(w, status, map[string]any{"error": map[string]string{"message": message}})
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(http.StatusMethodNotAllowed, "请使用 GET 检测或 POST 同步 ZCode")
		return
	}
	if !localClient(r) {
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{"available": false, "reason": "请在网关所在电脑打开控制台后配置本机 ZCode"})
		} else {
			fail(http.StatusForbidden, "一键配置仅允许网关所在电脑的本机访问")
		}
		return
	}
	if r.Method == http.MethodGet {
		source, err := zcode.Read(a.zcodeConfigPath())
		count, enabled := 0, true
		if err == nil {
			count, enabled, err = zcode.Validate(source)
		}
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		writeJSON(w, map[string]any{"available": err == nil, "reason": reason, "configured_models": count, "provider_enabled": enabled, "profile": "~/.zcode/v2/provider_config.json"})
		return
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback()) {
			fail(http.StatusForbidden, "请从本机控制台配置 ZCode")
			return
		}
	}
	if mediaType != "application/json" || r.Header.Get("X-Agent2API-Local-Action") != "zcode-sync" {
		fail(http.StatusBadRequest, "请使用控制台的一键配置按钮同步 ZCode")
		return
	}
	var input struct{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(http.StatusBadRequest, "同步请求格式无效")
		return
	}
	if !a.zcodeMu.TryLock() {
		fail(http.StatusConflict, "ZCode 正在同步，请等待当前操作完成")
		return
	}
	defer a.zcodeMu.Unlock()
	// 目录请求之前读取原文件，防止等待期间覆盖用户在 ZCode 中的新修改。
	source, err := zcode.Read(a.zcodeConfigPath())
	if err == nil {
		_, _, err = zcode.Validate(source)
	}
	if err != nil {
		fail(http.StatusUnprocessableEntity, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	for _, rt := range a.hub.Platforms() {
		if verifier, ok := rt.Adapter.(connectionVerifier); ok {
			if err := verifier.VerifyConnection(ctx); err != nil {
				fail(http.StatusUnprocessableEntity, "平台 "+rt.ID+" 的连接认证未通过，请先在账号管理中检测连接；ZCode 配置已保留")
				return
			}
		}
	}
	if err = a.hub.RefreshAllPlatforms(ctx); err != nil {
		fail(http.StatusUnprocessableEntity, "免费模型目录刷新未完成，ZCode 配置已保留")
		return
	}
	statuses, refreshing := a.hub.CatalogStatuses()
	if refreshing {
		fail(http.StatusUnprocessableEntity, "模型目录仍在更新，请稍后重试")
		return
	}
	for _, status := range statuses {
		if status.Status != "ready" {
			fail(http.StatusUnprocessableEntity, "平台 "+status.Platform+" 的目录验证失败，请先恢复连接后重试；ZCode 配置已保留")
			return
		}
	}
	candidate, result, err := zcode.Prepare(source, a.clientModels(), a.localGatewayOrigin(), a.cfg.Auth.APIKey, time.Now())
	if err != nil {
		fail(http.StatusUnprocessableEntity, err.Error())
		return
	}
	if ctx.Err() != nil {
		fail(http.StatusRequestTimeout, "同步已超时，ZCode 配置已保留")
		return
	}
	result.BackupPath, err = zcode.Write(a.zcodeConfigPath(), source, candidate)
	if err != nil {
		fail(http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, result)
}
