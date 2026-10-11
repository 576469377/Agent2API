package workbuddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGlobalConfigSelectsWorkBuddyProductAndFiltersMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "WorkBuddy/") {
			t.Error("wrong product catalogue UA")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"models":[{"id":"gpt-chat","name":"GPT","maxInputTokens":1000,"maxOutputTokens":500},{"id":"gpt-image","tags":["text-to-image"]},{"id":"video","tags":["image-to-video"]}]}}`))
	}))
	defer srv.Close()
	a, err := New(Config{Platform: GlobalPlatform, BaseURL: srv.URL, CredentialPath: globalTestCredential(t, "www.workbuddy.ai")})
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "gpt-chat" {
		t.Fatalf("wrong catalogue: %+v, %v", models, err)
	}
}
