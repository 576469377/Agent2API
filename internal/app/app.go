// Package app 提供 HTTP 路由与编排。
//
// 编排层对协议是无感的：下游协议被收敛成 Protocol 接口，
// 上游平台被收敛成 adapter.Adapter 接口，两者都只通过 internal/llm 通信。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
	"github.com/576469377/Agent2API/internal/web"
)

// maxBodyBytes 限制请求体大小，防止恶意大 body 打爆内存。
const maxBodyBytes = 32 << 20

// StreamEncoder 把 IR 事件编码为下游 SSE 帧。
type StreamEncoder interface {
	Encode(ev llm.ResponseEvent) ([]common.SSEEvent, error)
}

// Protocol 是下游协议编解码契约。
type Protocol interface {
	Name() string
	DecodeRequest(body []byte) (llm.RequestMessages, error)
	NewStreamEncoder(model string, includeUsage bool) StreamEncoder
	EncodeFinal(msg *llm.AssistantMessage, model string) ([]byte, error)
	AppendSSE(dst []byte, name string, data []byte) []byte
	// StreamErrorEvents 为 true 表示该协议的客户端会把流内错误事件当作可重试信号。
	StreamErrorEvents() bool
}

// App 是 HTTP 应用。
type App struct {
	cfg     config.Config
	adapter adapter.Adapter
	logger  *log.Logger
	// metrics 采集请求量、成功率、延迟、token 等运行指标，供控制台展示。
	metrics *obs.Metrics
	// metricsFile 是指标落盘路径；空字符串表示不持久化。
	metricsFile string
	// createdAt 用于 /v1/models 的 created 字段稳定输出。
	createdAt int64
	startedAt time.Time
}

// New 构造应用。
func New(cfg config.Config, adp adapter.Adapter, logger *log.Logger) *App {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	now := time.Now()
	metrics := obs.New()
	if cfg.MetricsFile != "" {
		// 恢复历史累积指标：重启后控制台仍能看到之前的统计。
		// 失败不致命——宁可本次从零开始，也不阻断启动。
		if err := metrics.Load(cfg.MetricsFile); err != nil {
			logger.Printf("加载历史指标失败（已忽略，本次以空统计运行）: %v", err)
		}
	}
	return &App{
		cfg: cfg, adapter: adp, logger: logger,
		metrics: metrics, metricsFile: cfg.MetricsFile,
		createdAt: now.Unix(), startedAt: now,
	}
}

// StartMetricsPersistence 启动后台周期落盘，避免进程意外退出时丢失统计。
// metricsFile 为空（未配置持久化）时直接返回，不启动任何协程。
func (a *App) StartMetricsPersistence(interval time.Duration) {
	if a.metricsFile == "" {
		return
	}
	go func() {
		if interval <= 0 {
			interval = 30 * time.Second
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if err := a.metrics.Save(a.metricsFile); err != nil {
				a.logger.Printf("指标落盘失败: %v", err)
			}
		}
	}()
}

// PersistMetricsNow 立即把指标落盘，供关闭流程在退出前调用。
func (a *App) PersistMetricsNow() {
	if a.metricsFile == "" {
		return
	}
	if err := a.metrics.Save(a.metricsFile); err != nil {
		a.logger.Printf("指标落盘失败: %v", err)
	}
}

// Handler 返回路由。
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", a.withAuth(a.chatCompletions))
	mux.HandleFunc("/v1/responses", a.withAuth(a.responses))
	mux.HandleFunc("/v1/messages", a.withAuth(a.anthropicMessages))
	mux.HandleFunc("/v1/models", a.withAuth(a.models))
	mux.HandleFunc("/health", a.health)

	// 控制台管理接口
	mux.HandleFunc("/api/status", a.withAuth(a.apiStatus))
	mux.HandleFunc("/api/metrics", a.withAuth(a.apiMetrics))
	mux.HandleFunc("/api/platforms", a.withAuth(a.apiPlatforms))
	mux.HandleFunc("/api/models", a.withAuth(a.apiModels))
	mux.HandleFunc("/api/config", a.withAuth(a.apiConfig))

	// 内置网页控制台：/ 与 /static/
	mux.Handle("/static/", http.StripPrefix("/static/", web.Static()))
	mux.HandleFunc("/", a.indexOrNotFound)
	return mux
}

// indexOrNotFound: 根路径返回网页控制台，其余未知路径保持 404，
// 避免控制台把所有拼写错误的 API 路径都吞掉。
func (a *App) indexOrNotFound(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		web.Index(w, r)
		return
	}
	a.notFound(w, r)
}

// ───────────────────────── 中间件 ─────────────────────────

// withAuth 校验客户端凭据。未配置 API Key 时放行（本机使用场景）。
func (a *App) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.Auth.APIKey == "" {
			next(w, r)
			return
		}
		if key := extractClientKey(r); key == a.cfg.Auth.APIKey {
			next(w, r)
			return
		}
		common.WriteError(w, &llm.Failure{
			Code: "invalid_api_key", Message: "Invalid API key", ClientFixable: true,
		})
	}
}

// extractClientKey 兼容 OpenAI 的 Bearer 与 Anthropic 的 x-api-key。
func extractClientKey(r *http.Request) string {
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return v
	}
	if v := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return strings.TrimSpace(v[len("bearer "):])
	}
	return ""
}

// notFound 必须返回 404。
//
// 不能复用 common.WriteError：它会把 ClientFixable 统一映射成 400，
// 让「路径不存在」看起来像「请求参数有误」，掩盖真实原因。
func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	body, err := json.Marshal(common.BuildErrorPayload(&llm.Failure{
		Code: "not_found", Message: "未知路径: " + r.URL.Path, ClientFixable: true,
	}))
	if err != nil {
		body = []byte(`{"error":{"message":"not found","type":"invalid_request_error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(body)
}

// health 暴露服务状态，便于排查凭证问题。
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"platform": a.adapter.Name(),
		"time":     time.Now().Unix(),
	})
}

// models 输出模型清单。
func (a *App) models(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	models, err := a.adapter.ListModels(ctx)
	if err != nil {
		a.logger.Printf("模型清单获取失败: %v", err)
		common.WriteError(w, llm.Wrap(err))
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  a.createdAt,
			"owned_by": a.adapter.Name(),
		})
	}
	sort.Slice(data, func(i, j int) bool {
		return data[i]["id"].(string) < data[j]["id"].(string)
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// ───────────────────────── 协议入口 ─────────────────────────

func (a *App) chatCompletions(w http.ResponseWriter, r *http.Request) {
	a.serve(w, r, chatProtocol{})
}

func (a *App) responses(w http.ResponseWriter, r *http.Request) {
	a.serve(w, r, responsesProtocol{})
}

func (a *App) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	a.serve(w, r, anthropicProtocol{})
}

// serve 是三协议共用的唯一编排函数。
func (a *App) serve(w http.ResponseWriter, r *http.Request, proto Protocol) {
	start := time.Now()
	a.metrics.Begin()
	rec := &obs.Record{Time: start, Protocol: proto.Name(), Status: 200, OK: true}
	defer func() {
		rec.DurationMs = time.Since(start).Milliseconds()
		a.metrics.Record(*rec)
	}()

	if r.Method != http.MethodPost {
		rec.OK, rec.Status, rec.Error = false, 405, "仅支持 POST"
		common.WriteError(w, &llm.Failure{
			Code: "method_not_allowed", Message: "仅支持 POST", ClientFixable: true,
		})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		rec.OK, rec.Status, rec.Error = false, 413, "请求体过大或读取失败"
		common.WriteError(w, &llm.Failure{
			Code: "body_too_large", Message: "请求体过大或读取失败", Cause: err, ClientFixable: true,
		})
		return
	}

	req, err := proto.DecodeRequest(body)
	if err != nil {
		f := llm.Wrap(err)
		rec.OK, rec.Status, rec.Error = false, f.HTTPStatus(), f.Error()
		common.WriteError(w, f)
		return
	}
	rec.Model = req.Model
	rec.Stream = req.Stream

	if req.Stream {
		a.writeStream(w, r, proto, req, rec)
	} else {
		a.writeFinal(w, r, proto, req, rec)
	}
	a.logger.Printf("protocol=%s model=%s stream=%v dur_ms=%d ok=%v",
		proto.Name(), req.Model, req.Stream, time.Since(start).Milliseconds(), rec.OK)
}

// writeFinal 走非流式：上游恒为流式，这里在本地聚合成单个 JSON。
func (a *App) writeFinal(w http.ResponseWriter, r *http.Request, proto Protocol, req llm.RequestMessages, rec *obs.Record) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.RequestTimeout())
	defer cancel()

	stream, err := a.adapter.Stream(ctx, req)
	if err != nil {
		a.fail(rec, err)
		common.WriteError(w, llm.Wrap(err))
		return
	}
	defer func() {
		if c, ok := stream.(io.Closer); ok {
			_ = c.Close()
		}
	}()

	// 生成耗时只计聚合上游流这一段，不含建连与写回；它是 TPS 的分母。
	genStart := time.Now()
	msg, err := aggregate(ctx, stream)
	genMs := time.Since(genStart).Milliseconds()
	if err != nil {
		a.fail(rec, err)
		common.WriteError(w, llm.Wrap(err))
		return
	}
	rec.InputTokens = int64(msg.Usage.InputTokens)
	rec.OutputTokens = int64(msg.Usage.OutputTokens)
	rec.ReasoningTokens = int64(msg.Usage.ReasoningTokens)
	// 未产出 token 或耗时不足 1ms 的请求不计入 TPS，避免污染分母。
	if rec.OutputTokens > 0 && genMs > 0 {
		rec.GenMs = genMs
	}

	payload, err := proto.EncodeFinal(msg, req.Model)
	if err != nil {
		a.fail(rec, err)
		common.WriteError(w, llm.Wrap(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

// fail 把失败信息写入观测记录。
func (a *App) fail(rec *obs.Record, err error) {
	f := llm.Wrap(err)
	rec.OK = false
	rec.Status = f.HTTPStatus()
	rec.Error = f.Error()
}

// writeStream 走流式：逐帧编码并即时下发。
//
// committed 标记首字节是否已写出——一旦写出，HTTP 状态就是 200，
// 此后的错误只能降级为流内错误事件。
func (a *App) writeStream(w http.ResponseWriter, r *http.Request, proto Protocol, req llm.RequestMessages, rec *obs.Record) {
	stream, err := a.adapter.Stream(r.Context(), req)
	if err != nil {
		a.fail(rec, err)
		common.WriteError(w, llm.Wrap(err))
		return
	}
	defer func() {
		if c, ok := stream.(io.Closer); ok {
			_ = c.Close()
		}
	}()

	enc := proto.NewStreamEncoder(req.Model, req.IncludeUsage)
	common.WriteSSEHeaders(w)

	flusher, canFlush := w.(http.Flusher)
	committed := false
	dst := make([]byte, 0, 4096)
	// 解码循环的起点，用作 TPS 的分母起点。
	// 注意：这段区间含客户端背压时间（w.Write / Flush 阻塞），慢客户端会低估 TPS。
	// 这是有意的——真实慢消费就是解码停顿，不额外补偿。
	genStart := time.Now()

	for {
		ev, err := stream.Recv(r.Context())
		if err != nil {
			if errors.Is(err, llm.ErrStreamDone) {
				break
			}
			failure := llm.Wrap(err)
			a.fail(rec, failure)
			a.logger.Printf("上游流错误: %s", failure)
			if !committed {
				common.WriteError(w, failure)
				return
			}
			// 已经写出过数据：只能在流内表达错误。
			errEvents, _ := enc.Encode(llm.ResponseEvent{Type: llm.EventError, Error: failure})
			for _, sse := range errEvents {
				dst = proto.AppendSSE(dst, sse.Name, sse.Data)
			}
			if len(dst) > 0 {
				_, _ = w.Write(dst)
				if canFlush {
					flusher.Flush()
				}
			}
			return
		}

		if ev.Type == llm.EventDone && ev.Usage != nil {
			rec.InputTokens = int64(ev.Usage.InputTokens)
			rec.OutputTokens = int64(ev.Usage.OutputTokens)
			rec.ReasoningTokens = int64(ev.Usage.ReasoningTokens)
			// 用法事件到达即视为生成结束；未产出 token 或不足 1ms 的不计入 TPS。
			if ms := time.Since(genStart).Milliseconds(); rec.OutputTokens > 0 && ms > 0 {
				rec.GenMs = ms
			}
		}

		events, err := enc.Encode(ev)
		if err != nil {
			a.logger.Printf("编码事件失败: %v", err)
			continue
		}
		for _, sse := range events {
			dst = proto.AppendSSE(dst, sse.Name, sse.Data)
		}
		if len(dst) == 0 {
			continue
		}
		// 批次合并：一次 Write + Flush 下发当前所有待发帧。
		if _, err := w.Write(dst); err != nil {
			return
		}
		committed = true
		dst = dst[:0]
		if canFlush {
			flusher.Flush()
		}
	}
	if len(dst) > 0 {
		_, _ = w.Write(dst)
		if canFlush {
			flusher.Flush()
		}
	}
}

// aggregate 把事件流聚合成完整消息。
//
// 上游不支持非流式，因此非流式响应必须在这里合成——这也意味着
// 非流式客户端的实际首字延迟与流式一致。
func aggregate(ctx context.Context, stream llm.ResponseStream) (*llm.AssistantMessage, error) {
	type block struct {
		kind     llm.ContentType
		text     strings.Builder
		thinking strings.Builder
		toolID   string
		toolName string
		toolArgs strings.Builder
	}
	blocks := map[int]*block{}
	order := make([]int, 0, 4)

	get := func(idx int, kind llm.ContentType) *block {
		if b, ok := blocks[idx]; ok {
			return b
		}
		b := &block{kind: kind}
		blocks[idx] = b
		order = append(order, idx)
		return b
	}

	msg := &llm.AssistantMessage{StopReason: llm.StopReasonStop}

	for {
		ev, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, llm.ErrStreamDone) {
				break
			}
			return nil, err
		}
		switch ev.Type {
		case llm.EventStart:
			if ev.Model != "" {
				msg.Model = ev.Model
			}
			if ev.ResponseID != "" {
				msg.ResponseID = ev.ResponseID
			}
		case llm.EventTextStart:
			get(ev.ContentIndex, llm.ContentText)
		case llm.EventTextDelta:
			get(ev.ContentIndex, llm.ContentText).text.WriteString(ev.Delta)
		case llm.EventThinkingStart:
			get(ev.ContentIndex, llm.ContentThinking)
		case llm.EventThinkingDelta:
			get(ev.ContentIndex, llm.ContentThinking).thinking.WriteString(ev.Delta)
		case llm.EventToolCallStart:
			b := get(ev.ContentIndex, llm.ContentToolCall)
			b.toolID = ev.ToolCallID
			b.toolName = ev.ToolName
		case llm.EventToolCallDelta:
			get(ev.ContentIndex, llm.ContentToolCall).toolArgs.WriteString(ev.Delta)
		case llm.EventToolCallEnd:
			b := get(ev.ContentIndex, llm.ContentToolCall)
			if ev.ToolCallID != "" {
				b.toolID = ev.ToolCallID
			}
			if ev.ToolName != "" {
				b.toolName = ev.ToolName
			}
			if ev.ToolArguments != "" {
				b.toolArgs.Reset()
				b.toolArgs.WriteString(ev.ToolArguments)
			}
		case llm.EventDone:
			if ev.Usage != nil {
				msg.Usage = *ev.Usage
			}
			if ev.StopReason != "" {
				msg.StopReason = ev.StopReason
			}
			if ev.Model != "" {
				msg.Model = ev.Model
			}
			if ev.ResponseID != "" {
				msg.ResponseID = ev.ResponseID
			}
		case llm.EventError:
			if ev.Error != nil {
				return nil, ev.Error
			}
			return nil, &llm.Failure{Code: "upstream_error", Message: "上游返回错误", UpstreamFault: true}
		}
	}

	sort.Ints(order)
	for _, idx := range order {
		b := blocks[idx]
		switch b.kind {
		case llm.ContentText:
			msg.Content = append(msg.Content, llm.Content{Type: llm.ContentText, Text: b.text.String()})
		case llm.ContentThinking:
			msg.Content = append(msg.Content, llm.Content{Type: llm.ContentThinking, Thinking: b.thinking.String()})
		case llm.ContentToolCall:
			args := b.toolArgs.String()
			if args == "" {
				args = "{}"
			}
			msg.Content = append(msg.Content, llm.Content{
				Type:     llm.ContentToolCall,
				ToolCall: &llm.ToolCall{ID: b.toolID, Name: b.toolName, Arguments: args},
			})
		}
	}
	if msg.StopReason == "" {
		msg.StopReason = llm.StopReasonStop
	}
	return msg, nil
}
