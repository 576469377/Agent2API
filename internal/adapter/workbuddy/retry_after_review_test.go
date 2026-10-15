package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

func TestRetryAfterHeaderFormats(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 500000000, time.UTC)
	for _, tc := range []struct {
		header string
		want   int
	}{
		{"42", 42}, {" 42 ", 42}, {"0", 0}, {"-1", 0}, {"42garbage", 0}, {"+42", 0},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0},
		{now.Add(48 * time.Hour).Format(http.TimeFormat), 86400},
		{"9223372036854775807", 86400}, {"not-a-date", 0},
	} {
		if got := parseRetryAfter(tc.header, now); got != tc.want {
			t.Fatalf("header=%q got=%d want=%d", tc.header, got, tc.want)
		}
	}
	f := llm.Wrap(httpError(429, []byte(`{"msg":"slow down"}`), "42"))
	if !f.RateLimited || f.RetryAfterSeconds != 42 {
		t.Fatalf("HTTP error lost scheduling advice: %+v", f)
	}
}

func TestResetDeadlinesAcrossRegions(t *testing.T) {
	for _, tc := range []struct{ message, utc string }{
		{"usage exceeds frequency limit, but don't worry, your usage will reset at 2026-10-16 03:35:37 UTC+8, alternatively, you can switch to the other models to continue using it.", "2026-10-15T19:35:37Z"},
		{"您的使用量已超出频率限制，将在 2026-10-16 03:35:37 UTC+8 重置，您也可以切换其他模型继续使用。", "2026-10-15T19:35:37Z"},
		{"将在 2026-10-16 03:35:37 重置", "2026-10-15T19:35:37Z"},
		{"Your usage WILL RESET AT 2026-10-16 03:35:37 UTC+08:00.", "2026-10-15T19:35:37Z"},
		{"will reset at 2026-10-16 03:35:37 GMT-04:30, switch models", "2026-10-16T08:05:37Z"},
		{"will reset at 2026-10-16 03:35:37 UTC", "2026-10-16T03:35:37Z"},
		{"will reset at 2026-10-16 03:35:37 GMT+0000", "2026-10-16T03:35:37Z"},
	} {
		expected, err := time.Parse(time.RFC3339, tc.utc)
		if err != nil {
			t.Fatal(err)
		}
		if got := resetEpochFromMsg(tc.message); int64(got) != expected.Unix() {
			t.Errorf("message=%q got=%d want=%d", tc.message, got, expected.Unix())
		}
	}
	for _, message := range []string{
		"proxy traffic quota exhausted at 2026-10-16 03:35:37 UTC+8",
		"will reset at 2026-10-16 03:35:37",
		"will reset at 2026-02-30 03:35:37 UTC+8",
		"will reset at 2026-10-16 03:35:37 UTC+25",
		"will reset at 2026-10-16 03:35:37 UTC+8:99",
		"will reset at 2026-10-16 03:35:37 UTC+8:xx",
		"will reset at 2026-10-16 03:35:37 UTC+bogus",
		"将在 2026-10-16 03:35:37 UTC+8 进行维护",
	} {
		if got := resetEpochFromMsg(message); got != 0 {
			t.Errorf("invalid reset message=%q got=%d", message, got)
		}
	}
}

func TestEnglishResetCreatesModelCooldownAndStopsUpstreamRetries(t *testing.T) {
	deadline := time.Now().Add(3 * time.Hour).In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02 15:04:05")
	adp, hits, _ := newRetryTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"msg": "usage exceeds frequency limit, your usage will reset at " + deadline + " UTC+8, alternatively, you can switch to the other models to continue using it."})
	})
	pool := adapter.NewPool("workbuddy-global", nil)
	pool.Add("test-account", adp)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		_, err := pool.Stream(ctx, llm.RequestMessages{Model: "deepseek-v4.1-flash", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "test"}}}}})
		if err == nil {
			t.Fatal("429 must fail")
		}
		failure := llm.Wrap(err)
		if !failure.RateLimited || failure.RetryAfterSeconds < 2*3600 {
			t.Fatalf("precise reset lost: %+v", failure)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("cooled model was retried upstream %d times", hits.Load())
	}
	status := pool.Statuses()[0]
	if !status.Healthy || status.ModelCooldowns["deepseek-v4.1-flash"] < 2*3600 || len(status.ModelCooldowns) != 1 {
		t.Fatalf("wrong cooldown scope: %+v", status)
	}
}

func TestResetHeaderPrecedenceAndNonRateErrors(t *testing.T) {
	message := []byte(`{"msg":"your usage will reset at 2099-01-01 00:00:00 UTC+8"}`)
	if f := llm.Wrap(httpError(429, message, "42")); f.RetryAfterSeconds != 42 {
		t.Fatalf("header should win: %+v", f)
	}
	if f := llm.Wrap(httpError(502, message, "")); f.RetryAfterSeconds != 0 {
		t.Fatalf("non-rate error must not trigger cooldown: %+v", f)
	}
	if f := llm.Wrap(httpError(429, []byte(`{"msg":"your usage will reset at 2000-01-01 00:00:00 UTC+8"}`), "")); f.RetryAfterSeconds != 0 {
		t.Fatalf("past deadline must not create cooldown: %+v", f)
	}
}
