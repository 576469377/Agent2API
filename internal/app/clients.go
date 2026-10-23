package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/576469377/Agent2API/internal/clientconfig"
)

func (a *App) clientModels() []clientconfig.Model {
	out := []clientconfig.Model{}
	for _, m := range a.hub.CachedModels() {
		out = append(out, clientconfig.Model{ModelInfo: m.ModelInfo, Platform: m.Platform, RoutingID: m.RoutingID, DefaultRoute: m.DefaultRoute})
	}
	return out
}

type clientInput struct {
	Platforms []string `json:"platforms"`
	Apps      []string `json:"apps,omitempty"`
}

func (a *App) clientPath(client string) string {
	if client == "pi" {
		if a.piPath != "" {
			return a.piPath
		}
		return clientconfig.PiPath()
	}
	if a.ccSwitchPath != "" {
		return a.ccSwitchPath
	}
	return clientconfig.CCSwitchPath()
}

func clientFail(w http.ResponseWriter, status int, message string) {
	writeJSONWithStatus(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func (a *App) apiPi(w http.ResponseWriter, r *http.Request)       { a.apiClient(w, r, "pi") }
func (a *App) apiCCSwitch(w http.ResponseWriter, r *http.Request) { a.apiClient(w, r, "cc-switch") }

func (a *App) apiClient(w http.ResponseWriter, r *http.Request, client string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		clientFail(w, http.StatusMethodNotAllowed, "请使用 GET 检测或 POST 配置客户端")
		return
	}
	if !localClient(r) {
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{"available": false, "reason": "请在网关所在电脑打开控制台后配置本机客户端"})
		} else {
			clientFail(w, http.StatusForbidden, "快捷配置仅允许网关所在电脑的本机访问")
		}
		return
	}
	path := a.clientPath(client)
	if r.Method == http.MethodGet {
		count, exists, reason := 0, true, ""
		var err error
		if client == "pi" {
			var raw []byte
			raw, err = clientconfig.ReadFile(path)
			exists = raw != nil
			if err == nil {
				count, err = clientconfig.ValidatePi(raw)
			}
		} else {
			var rows clientconfig.CCSnapshot
			rows, err = clientconfig.ReadCC(r.Context(), path)
			count = len(rows)
		}
		if err != nil {
			reason = err.Error()
		}
		writeJSON(w, map[string]any{"available": err == nil, "reason": reason, "configured_entries": count, "profile": path, "exists": exists})
		return
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback()) {
			clientFail(w, http.StatusForbidden, "请从本机控制台配置客户端")
			return
		}
	}
	if mediaType != "application/json" || r.Header.Get("X-Agent2API-Local-Action") != client+"-sync" {
		clientFail(w, http.StatusBadRequest, "请使用控制台的客户端配置按钮")
		return
	}
	var input clientInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || len(input.Platforms) == 0 || len(input.Platforms) > 16 || len(input.Apps) > 2 {
		clientFail(w, http.StatusBadRequest, "请选择上游平台，配置请求格式无效")
		return
	}
	configured := map[string]bool{}
	for _, rt := range a.hub.Platforms() {
		configured[rt.ID] = true
	}
	for _, p := range input.Platforms {
		if !configured[p] {
			clientFail(w, http.StatusBadRequest, "所选上游平台未配置，请先在账号管理中接入")
			return
		}
	}
	if client == "cc-switch" && len(input.Apps) == 0 {
		input.Apps = []string{"claude"}
	}
	for _, app := range input.Apps {
		if client != "cc-switch" || (app != "claude" && app != "codex") {
			clientFail(w, http.StatusBadRequest, "客户端应用选择无效")
			return
		}
	}
	if !a.clientSyncMu.TryLock() {
		clientFail(w, http.StatusConflict, "客户端配置正在写入，请等待当前操作完成")
		return
	}
	defer a.clientSyncMu.Unlock()
	var source []byte
	var snapshot clientconfig.CCSnapshot
	var err error
	if client == "pi" {
		source, err = clientconfig.ReadFile(path)
		if err == nil {
			_, err = clientconfig.ValidatePi(source)
		}
	} else {
		snapshot, err = clientconfig.ReadCC(r.Context(), path)
	}
	if err != nil {
		clientFail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err = a.verifyClientCatalog(ctx, input.Platforms); err != nil {
		clientFail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var result clientconfig.Result
	if client == "pi" {
		var candidate []byte
		candidate, result, err = clientconfig.PreparePi(source, a.clientModels(), input.Platforms, a.localGatewayOrigin(), a.cfg.Auth.APIKey, time.Now())
		if err == nil {
			if ctx.Err() != nil {
				err = fmt.Errorf("配置操作已超时，原文件已保留")
			} else {
				result.BackupPath, err = clientconfig.WriteFile(path, source, candidate)
			}
		}
	} else {
		result, err = clientconfig.SyncCC(ctx, path, snapshot, a.clientModels(), input.Platforms, input.Apps, a.localGatewayOrigin(), a.cfg.Auth.APIKey, time.Now())
	}
	if err != nil {
		clientFail(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, result)
}

func (a *App) verifyClientCatalog(ctx context.Context, platforms []string) error {
	wanted := map[string]bool{}
	for _, platform := range platforms {
		wanted[platform] = true
	}
	for _, rt := range a.hub.Platforms() {
		if !wanted[rt.ID] {
			continue
		}
		if verifier, ok := rt.Adapter.(connectionVerifier); ok {
			if err := verifier.VerifyConnection(ctx); err != nil {
				return fmt.Errorf("平台 %s 的连接认证未通过，请先检测账号连接；客户端配置已保留", rt.ID)
			}
		}
	}
	// 按稳定顺序刷新所选平台；未选平台故障不应阻断其他免费模型的配置。
	sort.Strings(platforms)
	for _, p := range platforms {
		if err := a.hub.RefreshPlatform(ctx, p); err != nil {
			return fmt.Errorf("平台 %s 免费目录刷新失败，客户端配置已保留", p)
		}
	}
	statuses, refreshing := a.hub.CatalogStatuses()
	if refreshing {
		return fmt.Errorf("模型目录仍在更新，请稍后重试")
	}
	for _, status := range statuses {
		if wanted[status.Platform] && status.Status != "ready" {
			return fmt.Errorf("平台 %s 免费目录验证失败，客户端配置已保留", status.Platform)
		}
	}
	return ctx.Err()
}
