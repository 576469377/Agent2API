package adapter

import (
	"context"
	"net/http/httptrace"

	"github.com/576469377/Agent2API/internal/llm"
)

// TraceRequest 仅观察传输阶段，不改变超时、请求内容或连接复用。
func TraceRequest(ctx context.Context) context.Context {
	if llm.ExecutionFrom(ctx) == nil {
		return ctx
	}
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteHeaders: func() { llm.SetAttemptPhase(ctx, "uploading") },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				llm.SetAttemptPhase(ctx, "waiting_headers")
			}
		},
	})
}
