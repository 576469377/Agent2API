package workbuddy

import (
	"net/http"
	"testing"
	"time"

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
