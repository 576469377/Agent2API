package workbuddy

import (
	"context"
	"net/http"

	"github.com/576469377/Agent2API/internal/llm"
)

func (a *Adapter) openChatAttempt(ctx context.Context, path string, body upstreamRequest, headers http.Header) (*http.Response, error) {
	attemptCtx, finish, err := llm.BeginAttempt(ctx, a.cfg.Platform, a.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	response, err := a.cli.openStream(attemptCtx, path, body, headers)
	status := 0
	if response != nil {
		status = response.StatusCode
	} else if err != nil {
		status = llm.Wrap(err).HTTPStatus()
	}
	err = finish(status, err)
	if err != nil && response != nil {
		response.Body.Close()
		response = nil
	}
	return response, err
}
