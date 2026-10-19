package workbuddy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/llm"
)

func TestBlockedRequestRetainsVendorCodeAndExplanation(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, message string
	}{
		{"localized", `{"code":11128,"msg":"request illegal","displayMsg":{"en":"Request blocked. Please send it again","zh":"请求被拦截，请重新发送"}}`, "upstream_11128", "请求被拦截，请重新发送"},
		{"english fallback", `{"code":11128,"msg":"request illegal","displayMsg":{"en":"Request blocked. Please send it again"}}`, "upstream_11128", "Request blocked. Please send it again"},
		{"different business code", `{"code":11140,"msg":"request illegal","displayMsg":{"zh":"账号存在异常行为，已被限制使用"}}`, "upstream_11140", "账号存在异常行为，已被限制使用"},
		{"unknown cause stays unknown", `{"msg":"request illegal"}`, "http_400", "request illegal"},
		{"malformed explanation", `{"code":11128,"msg":"request illegal","displayMsg":123}`, "upstream_11128", "request illegal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := llm.Wrap(httpError(http.StatusBadRequest, []byte(tc.body), ""))
			payload := common.BuildErrorPayload(failure)
			if failure.HTTPStatus() != 400 || payload.Error.Code != tc.code || !strings.Contains(payload.Error.Message, tc.message) {
				t.Fatalf("vendor refusal lost: %+v %+v", failure, payload)
			}
			if failure.RateLimited || failure.QuotaExhausted || failure.Unauthorized || shouldRetryDial(failure) {
				t.Fatalf("generic refusal acquired an unsupported cause/retry: %+v", failure)
			}
		})
	}
}

func TestUpstreamBlockedRequestIsNotReplayed(t *testing.T) {
	a, hits, _ := newRetryTestAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":11128,"msg":"request illegal","displayMsg":{"zh":"请求被拦截，请重新发送"}}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := a.Stream(ctx, llm.RequestMessages{Model: "test", SystemPrompt: "You are a helpful assistant.", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Hello"}}}}})
	if err == nil {
		t.Fatal("blocked request unexpectedly succeeded")
	}
	failure := llm.Wrap(err)
	if hits.Load() != 1 || failure.Code != "upstream_11128" || !strings.Contains(failure.Message, "请求被拦截") {
		t.Fatalf("refusal replayed or explanation lost: hits=%d failure=%+v", hits.Load(), failure)
	}
}

func TestTopLevelQuotaCodeKeepsQuotaClassification(t *testing.T) {
	failure := llm.Wrap(httpError(http.StatusBadRequest, []byte(`{"code":14018,"msg":"quota exhausted"}`), ""))
	if failure.Code != "upstream_14018" || !failure.QuotaExhausted || !failure.RateLimited || failure.HTTPStatus() != 429 {
		t.Fatalf("top-level quota code lost: %+v", failure)
	}
}
