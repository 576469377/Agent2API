package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
)

type imageCountingBlock struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	Output  json.RawMessage `json:"output"`
}

// Count the original content-block structure; never parse text, schemas, tool
// arguments or base64 data as image blocks. Invalid image sources still count.
func countWireImages(body []byte, protocol string) int {
	var root struct {
		Messages []imageCountingBlock `json:"messages"`
		Input    json.RawMessage      `json:"input"`
	}
	if json.Unmarshal(body, &root) != nil {
		return 0
	}
	imageType := map[string]string{"anthropic-messages": "image", "openai-chat": "image_url", "openai-responses": "input_image"}[protocol]
	if imageType == "" {
		return 0
	}
	var count func(json.RawMessage) int
	count = func(raw json.RawMessage) int {
		var blocks []imageCountingBlock
		if json.Unmarshal(raw, &blocks) != nil {
			return 0 // Text is opaque, even when it happens to contain JSON.
		}
		total := 0
		for _, block := range blocks {
			switch block.Type {
			case imageType:
				total++
			case "tool_result", "message", "":
				total += count(block.Content)
			case "function_call_output":
				total += count(block.Output)
			}
		}
		return total
	}
	total := 0
	for _, message := range root.Messages {
		total += count(message.Content)
	}
	return total + count(root.Input)
}

func requestImageCount(req llm.RequestMessages) int {
	if req.InputImageCount != nil {
		return *req.InputImageCount
	}
	return decodedImageCount(req)
}

func decodedImageCount(req llm.RequestMessages) int {
	// Internal callers without a wire request use the decoded content instead.
	var count func([]llm.Content) int
	count = func(blocks []llm.Content) int {
		total := 0
		for _, block := range blocks {
			if block.Type == llm.ContentImage {
				total++
			} else if block.Type == llm.ContentToolResult && block.ToolResult != nil {
				total += count(block.ToolResult.Blocks)
			}
		}
		return total
	}
	total := 0
	for _, message := range req.Messages {
		total += count(message.Content)
	}
	return total
}

func imageLimitFailure(platform, model string, current, limit int) *llm.Failure {
	return &llm.Failure{Code: "image_count_exceeded", ClientFixable: true,
		Message: fmt.Sprintf("模型 %s/%s 的单请求图片数量护栏为 %d 张，当前请求包含 %d 张（含历史消息及工具结果中的图片）。请开新对话、换模型或减少历史图片后重试；网关未裁剪请求内容。", platform, model, limit, current)}
}

func (a *App) openPlatformStream(ctx context.Context, rt *PlatformRuntime, req llm.RequestMessages, records ...*obs.Record) (llm.ResponseStream, error) {
	count := requestImageCount(req)
	policy := a.cfg.Models.ImageOverflowPolicyFor(rt.ID, req.Model)
	var rec *obs.Record
	if len(records) > 0 && records[0] != nil {
		rec = records[0]
		rec.ForwardedImageCount = nil
		rec.RemovedImageCount = 0
		rec.ImageOverflowPolicy = policy
	}
	if limit := a.cfg.Models.ImageLimitFor(rt.ID, req.Model); limit != nil && limit.Enabled && count > limit.MaxImages {
		if policy != config.ImageOverflowTrimOldest {
			return nil, imageLimitFailure(rt.ID, req.Model, count, limit.MaxImages)
		}
		if decodedImageCount(req) != count {
			return nil, &llm.Failure{Code: "image_cleanup_unavailable", ClientFixable: true,
				Message: fmt.Sprintf("模型 %s/%s 当前请求统计为 %d 张图片，护栏为 %d 张，但解码后的图片数量与原请求不一致。网关未自动清理；请检查请求结构或图片数据后重试。", rt.ID, req.Model, count, limit.MaxImages)}
		}
		prepared, removed := trimOldestRequestImages(req, limit.MaxImages)
		req = prepared
		if rec != nil {
			rec.RemovedImageCount = removed
		}
	}
	forwarded := decodedImageCount(req)
	if rec != nil {
		rec.ForwardedImageCount = &forwarded
	}
	return rt.Adapter.Stream(ctx, req)
}

func writeImageCleanupHeaders(w http.ResponseWriter, rec *obs.Record) {
	if rec.RemovedImageCount > 0 && rec.ForwardedImageCount != nil {
		w.Header().Set("X-Agent2API-Images-Removed", strconv.Itoa(rec.RemovedImageCount))
		w.Header().Set("X-Agent2API-Images-Forwarded", strconv.Itoa(*rec.ForwardedImageCount))
		w.Header().Set("X-Agent2API-Image-Overflow-Policy", rec.ImageOverflowPolicy)
	}
}

func (a *App) setRouteRecord(rec *obs.Record, rt *PlatformRuntime) {
	rec.Platform = rt.ID
	if limit := a.cfg.Models.ImageLimitFor(rt.ID, rec.Model); limit != nil {
		value := limit.MaxImages
		rec.MaxImages = &value
	}
}

// These messages are ambiguous: Step's wording also occurs with corrupt images.
// Only a known, enabled limit AND an excessive count can diagnose excess images.
func (a *App) explainImageFailure(err error, platform, model string, count int) *llm.Failure {
	f := llm.Wrap(err)
	if f.Code == "image_count_exceeded" || f.HTTPStatus() != 400 {
		return f
	}
	message := strings.ToLower(strings.TrimSpace(f.Message))
	ambiguous := false
	for _, phrase := range []string{"invalid request parameters", "the request parameters were rejected by the model provider", "please start a new conversation, replace the image, and try again."} {
		if strings.Contains(message, phrase) {
			ambiguous = true
			break
		}
	}
	if !ambiguous {
		return f
	}
	limit := a.cfg.Models.ImageLimitFor(platform, model)
	if limit != nil && limit.Enabled && count > limit.MaxImages {
		return imageLimitFailure(platform, model, count, limit.MaxImages)
	}
	copy := *f // Do not mutate a shared adapter error or infer an image-data diagnosis.
	copy.Message += fmt.Sprintf("（原因未知：处理后的请求包含 %d 张图片；请检查图片数据及其他请求参数。不能仅凭此文案认定图片超限。）", count)
	return &copy
}
