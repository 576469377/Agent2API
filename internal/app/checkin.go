package app

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/576469377/Agent2API/internal/adapter"
)

type checkinPlatform = adapter.CheckinProvider

func (a *App) StartPlatformBackground(ctx context.Context) {
	a.accountBackground.Do(func() { go a.runAccountBackground(ctx) })
}

func (a *App) apiAccountCheckin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "仅支持 GET / POST", http.StatusMethodNotAllowed)
		return
	}
	selected, ok := a.accountForBenefits(w, r)
	if !ok {
		return
	}
	p, ok := selected.(checkinPlatform)
	if !ok {
		http.Error(w, "此平台未提供签到功能", http.StatusBadRequest)
		return
	}
	var err error
	if r.Method == http.MethodGet {
		if r.URL.Query().Get("refresh") == "1" {
			err = p.RefreshCheckin(r.Context())
		}
	} else {
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if media != "application/json" {
			http.Error(w, "请求必须使用 application/json", http.StatusUnsupportedMediaType)
			return
		}
		var input struct {
			Action  string `json:"action"`
			Enabled *bool  `json:"enabled,omitempty"`
			Time    string `json:"time,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "签到配置格式无效", http.StatusBadRequest)
			return
		}
		switch input.Action {
		case "configure":
			if input.Enabled == nil {
				http.Error(w, "缺少自动签到开关", http.StatusBadRequest)
				return
			}
			err = p.ConfigureCheckin(*input.Enabled, input.Time)
		case "claim":
			if input.Enabled != nil || input.Time != "" {
				http.Error(w, "立即签到不接受配置字段", http.StatusBadRequest)
				return
			}
			err = p.ClaimCheckin(r.Context())
		default:
			http.Error(w, "未知签到操作", http.StatusBadRequest)
			return
		}
	}
	if err != nil {
		writeJSONWithStatus(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{"message": "签到操作失败，请检查登录状态或稍后重试"}, "checkin": p.CheckinStatus()})
		return
	}
	writeJSON(w, map[string]any{"checkin": p.CheckinStatus()})
}
