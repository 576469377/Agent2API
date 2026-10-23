package app

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

type connectionVerifier interface{ VerifyConnection(context.Context) error }
type apiKeySaver interface {
	SaveAPIKey(context.Context, string) error
}

func (a *App) apiAccountsConnect(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, message string, saved bool) {
		writeJSONWithStatus(w, status, map[string]any{"error": map[string]string{"message": message}, "saved": saved})
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(http.StatusMethodNotAllowed, "请使用 POST 配置或检测连接", false)
		return
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		fail(http.StatusUnsupportedMediaType, "请求必须使用 application/json", false)
		return
	}
	rt, ok := a.platformOr400(w, r)
	if !ok {
		return
	}
	var input struct {
		Action string `json:"action"`
		APIKey string `json:"api_key,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(http.StatusBadRequest, "连接配置格式无效", false)
		return
	}
	setup := adapter.SetupFor(rt.ID)
	if setup == nil || (setup.Method != "api_key" && setup.Method != "desktop" && setup.Method != "web_oauth" && setup.Method != "cli_oauth") {
		fail(http.StatusBadRequest, "请使用此平台的账号授权入口", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 28*time.Second)
	defer cancel()
	saved := false
	switch input.Action {
	case "save_key":
		saver, ok := rt.Adapter.(apiKeySaver)
		if setup.Method != "api_key" || !ok {
			fail(http.StatusBadRequest, "此平台不支持保存 API Key", false)
			return
		}
		if err := saver.SaveAPIKey(ctx, input.APIKey); err != nil {
			fail(http.StatusUnprocessableEntity, err.Error(), false)
			return
		}
		saved = true
	case "check":
		if input.APIKey != "" {
			fail(http.StatusBadRequest, "检测现有连接时不应提交新密钥", false)
			return
		}
		if verifier, ok := rt.Adapter.(connectionVerifier); ok {
			if err := verifier.VerifyConnection(ctx); err != nil {
				fail(http.StatusUnprocessableEntity, err.Error(), false)
				return
			}
		}
	default:
		fail(http.StatusBadRequest, "未知的连接操作", false)
		return
	}
	if err := a.hub.RefreshPlatform(ctx, rt.ID); err != nil {
		message := err.Error()
		if setup.Method == "api_key" {
			message = "密钥已通过验证，但免费模型目录刷新失败；请稍后重新检测连接"
			if saved {
				message = "密钥已保存，但免费模型目录刷新失败；请稍后重新检测连接"
			}
		}
		fail(http.StatusUnprocessableEntity, message, saved)
		return
	}
	count := 0
	for _, model := range a.hub.CachedModels() {
		if model.Platform == rt.ID {
			count++
		}
	}
	writeJSON(w, map[string]any{"status": "connected", "platform": rt.ID, "model_count": count, "saved": saved})
}
