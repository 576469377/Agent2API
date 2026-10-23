// Package app 提供 HTTP 路由与编排。
//
// 编排层对协议是无感的：下游协议被收敛成 Protocol 接口，
// 上游平台被收敛成 adapter.Adapter 接口，两者都只通过 internal/llm 通信。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
	"github.com/576469377/Agent2API/internal/web"
)

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
	cfg config.Config
	// hub 集成了本进程要服务的全部上游平台；请求按模型名路由到对应平台。
	hub    *Hub
	logger *log.Logger
	// metrics 采集请求量、成功率、延迟、token 等运行指标，供控制台展示。
	metrics *obs.Metrics
	// metricsFile 是指标落盘路径；空字符串表示不持久化。
	metricsFile    string
	metricsStorage *metricsStorage
	// createdAt 用于 /v1/models 的 created 字段稳定输出。
	createdAt int64
	startedAt time.Time
	// logins 管理进行中的设备码登录（控制台「添加账号」「重新登录」）。
	// 会话按平台标记；不同平台的登录方式在 loginManager 内部分派。
	logins            *loginManager
	zcodeMu           sync.Mutex
	zcodePath         string
	clientSyncMu      sync.Mutex
	piPath            string
	ccSwitchPath      string
	accountBackground sync.Once
}

// New 构造应用。hub 由装配层（cmd）按配置建好所有平台后传入。
func New(cfg config.Config, hub *Hub, logger *log.Logger) *App {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	now := time.Now()
	metrics := obs.New()
	metricsFile, storage := loadMetricsStorage(metrics, cfg.MetricsFile, logger)
	var historyLocation *time.Location
	if cfg.MetricsHistoryLogTimezone != "" {
		if stamp, err := time.Parse("-07:00", cfg.MetricsHistoryLogTimezone); err == nil {
			_, offset := stamp.Zone()
			historyLocation = time.FixedZone("history", offset)
		}
	}
	for _, path := range cfg.MetricsHistoryImports {
		if path == cfg.MetricsFile {
			continue
		}
		if err := metrics.ImportHistory(path, historyLocation); err != nil {
			logger.Printf("导入历史指标失败（保留既有统计）: %v", err)
		}
	}
	a := &App{
		cfg: cfg, hub: hub, logger: logger,
		metrics: metrics, metricsFile: metricsFile, metricsStorage: storage,
		createdAt: now.Unix(), startedAt: now,
	}
	a.logins = newLoginManager(func(f string, args ...any) { logger.Printf(f, args...) })
	a.logins.replaceCredential = a.replaceLoginCredential
	return a
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
			a.saveMetrics()
		}
	}()
}

// PersistMetricsNow 立即把指标落盘，供关闭流程在退出前调用。
func (a *App) PersistMetricsNow() {
	a.saveMetrics()
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
	// /api/auth-hint 刻意不鉴权：只暴露「是否启用了鉴权」布尔，供控制台在
	// 401 时引导用户输入密钥；其余 /api/* 都走 withAuth。
	mux.HandleFunc("/api/auth-hint", a.apiAuthHint)
	mux.HandleFunc("/api/status", a.withAuth(a.apiStatus))
	mux.HandleFunc("/api/metrics", a.withAuth(a.apiMetrics))
	mux.HandleFunc("/api/live", a.withAuth(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.metrics.LiveSnapshot()) }))
	mux.HandleFunc("/api/platforms", a.withAuth(a.apiPlatforms))
	mux.HandleFunc("/api/accounts", a.withAuth(a.apiAccounts))
	mux.HandleFunc("/api/accounts/manage", a.withAuth(a.apiAccountsManage))
	mux.HandleFunc("/api/accounts/connect", a.withAuth(a.apiAccountsConnect))
	mux.HandleFunc("/api/accounts/checkin", a.withAuth(a.apiAccountCheckin))
	mux.HandleFunc("/api/accounts/benefits", a.withAuth(a.apiAccountBenefits))
	mux.HandleFunc("/api/accounts/login-status", a.withAuth(a.apiLoginStatus))
	mux.HandleFunc("/api/accounts/models", a.withAuth(a.apiAccountModels))
	mux.HandleFunc("/api/models", a.withAuth(a.apiModels))
	mux.HandleFunc("/api/config", a.withAuth(a.apiConfig))
	mux.HandleFunc("/api/clients/zcode", a.withAuth(a.apiZCode))
	mux.HandleFunc("/api/clients/pi", a.withAuth(a.apiPi))
	mux.HandleFunc("/api/clients/cc-switch", a.withAuth(a.apiCCSwitch))
	// /metrics 用 Prometheus 文本格式暴露同一份指标（见 apiPrometheus）。
	// 与控制台接口一样走 withAuth：抓取端用 Authorization: Bearer <api_key> 即可。
	mux.HandleFunc("/metrics", a.withAuth(a.apiPrometheus))

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
		// NewFailure 立即派生分类：Unauthorized → HTTPStatus 401。
		// 不能直接给字面量——派生字段全为零值时会被映射成 400，
		// 客户端 SDK 靠 401 才知道该重新配置密钥而不是改请求体。
		common.WriteError(w, llm.NewFailure("invalid_api_key", "Invalid API key", nil))
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
	ids := make([]string, 0, 4)
	for _, rt := range a.hub.Platforms() {
		ids = append(ids, rt.ID)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    "ok",
		"platform":  ids[0], // 兼容单平台客户端：默认平台
		"platforms": ids,
		"time":      time.Now().Unix(),
	})
}

// models 输出模型清单。多平台集成后返回所有平台的模型合并目录，
// owned_by 标注来源平台；模型 ID 冲突时构造期已告警并保留首个平台。
func (a *App) models(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	models, err := a.hub.ListModels(ctx)
	if err != nil {
		a.logger.Printf("模型清单获取失败: %v", err)
		common.WriteError(w, llm.Wrap(err))
		return
	}
	data := make([]map[string]any, 0, len(models))
	seen := map[string]bool{}
	for _, m := range models {
		ids := []string{m.RoutingID}
		if m.DefaultRoute || m.RoutingID == "" {
			ids = append(ids, m.ID)
		}
		for _, id := range ids {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			data = append(data, map[string]any{
				"id":       id,
				"object":   "model",
				"created":  a.createdAt,
				"owned_by": m.Platform,
			})
		}
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
	execution := llm.NewExecution(a.cfg.Gateway.MaxAttempts())
	r = r.WithContext(llm.WithExecution(r.Context(), execution))
	w.Header().Set("X-Request-ID", execution.Snapshot().RequestID)
	liveID := a.metrics.BeginLive(proto.Name(), start)
	a.metrics.AttachExecution(liveID, execution)
	rec := &obs.Record{LiveID: liveID, Time: start, Protocol: proto.Name(), Status: 200, OK: true}
	defer func() {
		trace := execution.Snapshot()
		rec.RequestID, rec.AttemptCount, rec.QueueMs, rec.Attempts = trace.RequestID, trace.AttemptCount, trace.QueueMs, trace.Attempts
		rec.DurationMs = time.Since(start).Milliseconds()
		a.metrics.Record(*rec)
	}()

	if r.Method != http.MethodPost {
		rec.OK, rec.Status, rec.Error = false, 405, "仅支持 POST"
		// 不能走 common.WriteError：它按 ClientFixable 统一映射 400，
		// 会把「方法不对」伪装成「请求体有错」。405 是精确语义，
		// 且 RFC 7231 要求响应带 Allow 头告知可用方法。
		w.Header().Set("Allow", http.MethodPost)
		body, _ := json.Marshal(common.BuildErrorPayload(&llm.Failure{
			Code: "method_not_allowed", Message: "仅支持 POST", ClientFixable: true,
		}))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write(body)
		return
	}
	bodyLimit := a.cfg.Server.MaxBodyBytes()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
	if err != nil {
		var sizeError *http.MaxBytesError
		failure := &llm.Failure{Code: "body_read_failed", Message: "读取请求体失败", Cause: err, ClientFixable: true}
		if errors.As(err, &sizeError) {
			failure.Code = "body_too_large"
			failure.Message = fmt.Sprintf("请求体过大（传输上限 %d MiB）", bodyLimit>>20)
		}
		common.WriteError(w, a.fail(rec, failure))
		return
	}

	req, err := proto.DecodeRequest(body)
	if err != nil {
		f := llm.Wrap(err)
		rec.OK, rec.Status, rec.Error = false, f.HTTPStatus(), f.Error()
		common.WriteError(w, f)
		return
	}
	imageCount := countWireImages(body, proto.Name())
	execution.StartRouting(a.cfg.Gateway.RoutingTimeout())
	req.InputImageCount = &imageCount
	rec.ImageCount = &imageCount
	defer func() {
		forwarded := "not_forwarded"
		if rec.ForwardedImageCount != nil {
			forwarded = fmt.Sprint(*rec.ForwardedImageCount)
		}
		trace := execution.Snapshot()
		a.logger.Printf("request_id=%s protocol=%s platform=%s model=%s stream=%v images=%d images_forwarded=%s images_removed=%d image_policy=%s attempts=%d queue_ms=%d dur_ms=%d ok=%v status=%d",
			trace.RequestID, proto.Name(), rec.Platform, rec.Model, rec.Stream, imageCount, forwarded, rec.RemovedImageCount, rec.ImageOverflowPolicy, trace.AttemptCount, trace.QueueMs, time.Since(start).Milliseconds(), rec.OK, rec.Status)
	}()
	// 会话亲和的路由键：同一会话的多轮请求粘住同一账号。
	// 请求头优先（客户端显式声明），其次 body 里的稳定字段，详见 affinity.go。
	req.SessionKey = sessionKeyOf(body, r.Header)

	// 模型别名：客户端固定模型名 → 账号实际可用的模型（见 config.ModelsConfig）。
	// 命中别名时记录原始名，控制台据此显示「claude-* → glm-5.3」。
	model, platformHint := a.hub.ResolveAlias(req.Model)
	if model != req.Model {
		rec.ModelRequested = req.Model
		req.Model = model
	}
	rec.Model = req.Model
	rec.Stream = req.Stream
	a.metrics.RouteLive(rec.LiveID, platformHint, rec.Model)

	if req.Stream {
		a.writeStream(w, r, proto, req, rec, platformHint)
	} else {
		a.writeFinal(w, r, proto, req, rec, platformHint)
	}
}

// writeFinal 走非流式：上游恒为流式，这里在本地聚合成单个 JSON。
// platformHint 非空时优先路由到该平台（来自模型别名），否则按模型名路由。
func (a *App) writeFinal(w http.ResponseWriter, r *http.Request, proto Protocol, req llm.RequestMessages, rec *obs.Record, platformHint string) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.RequestTimeout())
	defer cancel()

	stream, rt, err := a.openRoutedStream(ctx, req, platformHint, rec)
	writeExecutionHeaders(w, ctx)
	a.setRouteRecord(rec, rt)
	writeImageCleanupHeaders(w, rec)
	if err != nil {
		common.WriteError(w, a.fail(rec, err))
		return
	}
	// 账号归因：控制台的「每账号用量」完全依赖这一行。
	rec.Account = adapter.AccountOf(stream)
	stream = &observedStream{inner: stream, metrics: a.metrics, record: rec}
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
		common.WriteError(w, a.fail(rec, err))
		return
	}
	rec.InputTokens = int64(msg.Usage.InputTokens)
	rec.OutputTokens = int64(msg.Usage.OutputTokens)
	rec.ReasoningTokens = int64(msg.Usage.ReasoningTokens)
	rec.CachedTokens = int64(msg.Usage.CachedTokens)
	rec.CacheWriteTokens = int64(msg.Usage.CacheWriteTokens)
	// 未产出 token 或耗时不足 1ms 的请求不计入 TPS，避免污染分母。
	if rec.OutputTokens > 0 && genMs > 0 {
		rec.GenMs = genMs
	}

	payload, err := proto.EncodeFinal(msg, req.Model)
	if err != nil {
		common.WriteError(w, a.fail(rec, err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(payload); err != nil {
		a.recordWriteFailure(rec)
	}
}

func (a *App) recordWriteFailure(rec *obs.Record) {
	if rec.OK {
		rec.OK = false
		rec.Status = 499
		rec.Error = "client_write_failed: 响应写回客户端失败"
		rec.ErrorCode = "client_write_failed"
	}
}

// fail 把失败信息写入观测记录。
func (a *App) fail(rec *obs.Record, err error) *llm.Failure {
	f := llm.Wrap(err)
	if rec.ImageCount != nil {
		count := *rec.ImageCount
		if rec.ForwardedImageCount != nil {
			count = *rec.ForwardedImageCount
		}
		f = a.explainImageFailure(f, rec.Platform, rec.Model, count)
	}
	rec.OK = false
	rec.Status = f.HTTPStatus()
	rec.Error = f.Error()
	rec.ErrorCode = f.Code
	return f
}

// writeStream 走流式：逐帧编码并即时下发。
//
// committed 标记首字节是否已写出——一旦写出，HTTP 状态就是 200，
// 此后的错误只能降级为流内错误事件。
// platformHint 非空时优先路由到该平台（来自模型别名），否则按模型名路由。
func (a *App) writeStream(w http.ResponseWriter, r *http.Request, proto Protocol, req llm.RequestMessages, rec *obs.Record, platformHint string) {
	stream, rt, err := a.openRoutedStream(r.Context(), req, platformHint, rec)
	writeExecutionHeaders(w, r.Context())
	a.setRouteRecord(rec, rt)
	writeImageCleanupHeaders(w, rec)
	if err != nil {
		common.WriteError(w, a.fail(rec, err))
		return
	}
	// 账号归因：控制台的「每账号用量」完全依赖这一行。
	rec.Account = adapter.AccountOf(stream)
	stream = &observedStream{inner: stream, metrics: a.metrics, record: rec}
	defer func() {
		if c, ok := stream.(io.Closer); ok {
			_ = c.Close()
		}
	}()

	enc := proto.NewStreamEncoder(req.Model, req.IncludeUsage)
	common.WriteSSEHeaders(w)

	_, canFlush := w.(http.Flusher)
	flush := func() bool {
		if canFlush {
			if err := http.NewResponseController(w).Flush(); err != nil {
				a.recordWriteFailure(rec)
				return false
			}
		}
		return true
	}
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
			failure := a.fail(rec, err)
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
				if _, err := w.Write(dst); err != nil {
					a.recordWriteFailure(rec)
					return
				}
				if !flush() {
					return
				}
			}
			return
		}

		if ev.Type == llm.EventDone && ev.Usage != nil {
			rec.InputTokens = int64(ev.Usage.InputTokens)
			rec.OutputTokens = int64(ev.Usage.OutputTokens)
			rec.ReasoningTokens = int64(ev.Usage.ReasoningTokens)
			rec.CachedTokens = int64(ev.Usage.CachedTokens)
			rec.CacheWriteTokens = int64(ev.Usage.CacheWriteTokens)
			// 用法事件到达即视为生成结束；未产出 token 或不足 1ms 的不计入 TPS。
			if ms := time.Since(genStart).Milliseconds(); rec.OutputTokens > 0 && ms > 0 {
				rec.GenMs = ms
			}
		}

		events, err := enc.Encode(ev)
		if err != nil {
			a.logger.Printf("编码事件失败: %v", err)
			a.fail(rec, &llm.Failure{Code: "encode_failed", Message: "响应事件编码失败", Cause: err})
			if !committed {
				common.WriteError(w, llm.Wrap(err))
			}
			return
		}
		for _, sse := range events {
			dst = proto.AppendSSE(dst, sse.Name, sse.Data)
		}
		if len(dst) == 0 {
			continue
		}
		// 批次合并：一次 Write + Flush 下发当前所有待发帧。
		if _, err := w.Write(dst); err != nil {
			a.recordWriteFailure(rec)
			return
		}
		committed = true
		dst = dst[:0]
		if !flush() {
			return
		}
	}
	if len(dst) > 0 {
		if _, err := w.Write(dst); err != nil {
			a.recordWriteFailure(rec)
			return
		}
		if !flush() {
			return
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
