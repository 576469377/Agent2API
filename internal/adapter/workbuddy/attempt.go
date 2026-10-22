package workbuddy

import (
	"context"
	"net/http"

	"github.com/576469377/Agent2API/internal/llm"
)

func (a *Adapter) openChatAttempt(ctx context.Context, path string, body upstreamRequest, headers http.Header) (*http.Response, error) {
	// RequestTimeout 是非流式请求的总超时，不能复用于多模态流的建连。
	// 大量历史图片的上传就可能超过它；等待由共享路由预算和流总时长约束。
	attemptCtx, finish, err := llm.BeginAttempt(ctx, a.cfg.Platform, 0)
	if err != nil {
		return nil, err
	}
	response, err := a.cli.openStream(attemptCtx, path, body, headers)
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	err = finish(status, err)
	if err != nil && response != nil {
		response.Body.Close()
		response = nil
	}
	return response, err
}
