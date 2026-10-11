package common

import (
	"net/http/httptest"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestWriteErrorPublishesKnownRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		failure *llm.Failure
		want    string
	}{
		{&llm.Failure{RateLimited: true, RetryAfterSeconds: 90}, "90"},
		{&llm.Failure{RateLimited: true}, ""},
		{&llm.Failure{Unauthorized: true, RetryAfterSeconds: 90}, ""},
		{&llm.Failure{Code: "no_available_accounts", RetryAfterSeconds: 5}, "5"},
	} {
		w := httptest.NewRecorder()
		WriteError(w, tc.failure)
		if got := w.Header().Get("Retry-After"); got != tc.want {
			t.Fatalf("status=%d retry-after=%q want %q", w.Code, got, tc.want)
		}
	}
}
