package kiro

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

type toolWait struct {
	id, name, arguments string
	result              chan *llm.ToolResult
	delivered, answered bool
}

// One native agent survives a downstream tool round trip. Its MCP request is
// held until that same caller returns the result; the gateway never runs tools.
type job struct {
	a                                     *Adapter
	ctx                                   context.Context
	cancel                                context.CancelFunc
	c                                     *rpcClient
	server                                *http.Server
	work, session, binding, prefix, model string
	prefixLen                             int
	tools                                 []llm.ToolDefinition
	mu                                    sync.Mutex
	sink                                  *jobStream
	waits                                 []*toolWait
	index                                 int
	textOpen                              bool
	closed                                bool
	requireTool                           bool
	closeOnce                             sync.Once
}

type jobStream struct {
	j                   *job
	events              chan llm.ResponseEvent
	end                 chan struct{}
	once                sync.Once
	mu                  sync.Mutex
	finished, handedOff bool
}

func (s *jobStream) AccountLabel() string { return "官方 CLI Free 账号" }

func randomID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("system random source unavailable")
	}
	return hex.EncodeToString(b)
}
func messageHash(m []llm.Message) string {
	copyMessages := append([]llm.Message(nil), m...)
	for i := range copyMessages {
		copyMessages[i].Content = withoutCacheHints(copyMessages[i].Content)
	}
	b, _ := json.Marshal(copyMessages)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func withoutCacheHints(blocks []llm.Content) []llm.Content {
	out := append([]llm.Content(nil), blocks...)
	for i := range out {
		out[i].CacheControl = nil
		if out[i].ToolResult != nil {
			result := *out[i].ToolResult
			result.Blocks = withoutCacheHints(result.Blocks)
			out[i].ToolResult = &result
		}
	}
	return out
}

func (j *job) attach() *jobStream {
	s := &jobStream{j: j, events: make(chan llm.ResponseEvent, 256), end: make(chan struct{})}
	j.sink = s
	j.index = 0
	j.textOpen = false
	j.send(llm.ResponseEvent{Type: llm.EventStart, Model: j.model, ResponseID: "kiro_" + randomID()})
	return s
}
func (s *jobStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	s.mu.Lock()
	finished := s.finished
	s.mu.Unlock()
	if finished {
		return llm.ResponseEvent{}, io.EOF
	}
	select {
	case e := <-s.events:
		if e.Type == llm.EventDone || e.Type == llm.EventError {
			s.mu.Lock()
			s.finished = true
			s.handedOff = e.Type == llm.EventDone && e.StopReason == llm.StopReasonToolCalls
			s.mu.Unlock()
			s.once.Do(func() { close(s.end) })
		}
		return e, nil
	case <-ctx.Done():
		s.j.close()
		return llm.ResponseEvent{}, ctx.Err()
	case <-s.j.ctx.Done():
		return llm.ResponseEvent{}, upstreamFailure("kiro_session_expired", "Kiro 工具会话已结束或超过等待时间，请重新发起请求")
	}
}
func (s *jobStream) Close() error {
	s.mu.Lock()
	keep := s.finished && s.handedOff
	s.mu.Unlock()
	s.once.Do(func() { close(s.end) })
	if !keep {
		s.j.close()
	}
	return nil
}

// Called under j.mu. Never wait indefinitely on a disconnected consumer.
func (j *job) send(e llm.ResponseEvent) {
	if j.sink == nil {
		return
	}
	select {
	case j.sink.events <- e:
	case <-j.sink.end:
	case <-j.ctx.Done():
	}
}
func (j *job) endText() {
	if j.textOpen {
		j.send(llm.ResponseEvent{Type: llm.EventTextEnd, ContentIndex: j.index})
		j.index++
		j.textOpen = false
	}
}
func (j *job) deliverWaiting() {
	if j.sink == nil {
		return
	}
	delivered := false
	for _, w := range j.waits {
		if w.delivered || w.answered {
			continue
		}
		j.endText()
		w.delivered = true
		delivered = true
		j.send(llm.ResponseEvent{Type: llm.EventToolCallStart, ContentIndex: j.index, ToolCallID: w.id, ToolName: w.name})
		j.send(llm.ResponseEvent{Type: llm.EventToolCallDelta, ContentIndex: j.index, ToolCallID: w.id, ToolName: w.name, Delta: w.arguments})
		j.send(llm.ResponseEvent{Type: llm.EventToolCallEnd, ContentIndex: j.index, ToolCallID: w.id, ToolName: w.name, ToolArguments: w.arguments, ToolArgumentsDone: true})
		j.index++
	}
	if delivered {
		j.send(llm.ResponseEvent{Type: llm.EventDone, StopReason: llm.StopReasonToolCalls})
		j.sink = nil
	}
}

func (a *Adapter) startJob(parent context.Context, r llm.RequestMessages) (llm.ResponseStream, error) {
	exe, err := ResolveCLI(a.cfg.CLIPath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.StreamTotalTimeout)
	j := &job{a: a, ctx: ctx, cancel: cancel, binding: requestBinding(r), prefix: messageHash(r.Messages), prefixLen: len(r.Messages), model: r.Model}
	j.requireTool = r.ToolChoice != nil && (r.ToolChoice.Mode == llm.ToolChoiceRequired || r.ToolChoice.Mode == llm.ToolChoiceFunction)
	for _, t := range r.Tools {
		if r.ToolChoice != nil && (r.ToolChoice.Mode == llm.ToolChoiceNone || (r.ToolChoice.Mode == llm.ToolChoiceFunction && t.Name != r.ToolChoice.FunctionName)) {
			continue
		}
		j.tools = append(j.tools, t)
	}
	work, err := prepareWorkspace(a.cfg.WorkRoot)
	if err != nil {
		cancel()
		return nil, clientFailure("kiro_workspace", "无法准备 Kiro 独立工作目录")
	}
	j.work = work
	fail := func(e error) (llm.ResponseStream, error) { j.close(); return nil, e }
	servers := []any{}
	if len(j.tools) > 0 {
		ln, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			return fail(upstreamFailure("kiro_tool_bridge", "无法启动本机工具桥接"))
		}
		path := "/mcp/" + randomID()
		mux := http.NewServeMux()
		mux.HandleFunc(path, j.serveMCP)
		j.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16384}
		go func() { _ = j.server.Serve(ln) }()
		servers = append(servers, map[string]any{"type": "http", "name": "agent2api", "url": "http://" + ln.Addr().String() + path, "headers": []any{}})
	}
	c, e := startRPC(ctx, exe, work, j.notify)
	if e != nil {
		return fail(e)
	}
	j.c = c
	// Setup is cancellable by the HTTP request, while a completed tool handoff is not.
	setup, stop := context.WithTimeout(parent, a.cfg.RequestTimeout)
	defer stop()
	session, available, e := initialize(setup, c, work, servers)
	if e != nil {
		return fail(e)
	}
	j.mu.Lock()
	j.session = session
	j.mu.Unlock()
	found := false
	for _, id := range available {
		found = found || id == r.Model
	}
	if !found {
		return fail(clientFailure("kiro_model_unavailable", "Kiro 当前会话不能选择该模型"))
	}
	u, e := readUsage(setup, c, session)
	if e != nil {
		return fail(e)
	}
	a.recordUsage(u)
	if _, e = verifyFree(u); e != nil {
		return fail(e)
	}
	if e = c.call(setup, "session/set_model", map[string]any{"sessionId": session, "modelId": r.Model}, nil); e != nil {
		return fail(e)
	}
	j.mu.Lock()
	s := j.attach()
	j.mu.Unlock()
	j.watch(parent, s)
	go func() {
		var reply struct {
			Stop string `json:"stopReason"`
		}
		err := c.call(ctx, "session/prompt", map[string]any{"sessionId": session, "prompt": []any{map[string]any{"type": "text", "text": renderPrompt(r)}}}, &reply)
		j.mu.Lock()
		if j.closed {
			j.mu.Unlock()
			return
		}
		current := j.sink
		j.endText()
		if err != nil {
			j.send(llm.ResponseEvent{Type: llm.EventError, Error: llm.Wrap(err), StopReason: llm.StopReasonError})
		} else if j.requireTool && len(j.waits) == 0 {
			j.send(llm.ResponseEvent{Type: llm.EventError, Error: upstreamFailure("kiro_tool_choice_unfulfilled", "Kiro 未执行要求的工具调用"), StopReason: llm.StopReasonError})
		} else if reply.Stop == "end_turn" {
			j.send(llm.ResponseEvent{Type: llm.EventDone, StopReason: llm.StopReasonStop})
		} else {
			j.send(llm.ResponseEvent{Type: llm.EventError, Error: upstreamFailure("kiro_turn_incomplete", "Kiro 未正常完成本轮生成"), StopReason: llm.StopReasonError})
		}
		j.mu.Unlock()
		// Let the reader consume the final event before cancelling its context.
		if current != nil {
			select {
			case <-current.end:
			case <-time.After(30 * time.Second):
			case <-ctx.Done():
			}
		}
		j.close()
	}()
	go func() { <-ctx.Done(); j.close() }()
	return s, nil
}

func (j *job) watch(ctx context.Context, s *jobStream) {
	go func() {
		select {
		case <-ctx.Done():
			select {
			case <-s.end:
				return
			default:
				j.close()
			}
		case <-s.end:
		case <-j.ctx.Done():
		}
	}()
}

func (j *job) resume(ctx context.Context, r llm.RequestMessages) (llm.ResponseStream, error) {
	if requestBinding(r) != j.binding || len(r.Messages) < j.prefixLen || messageHash(r.Messages[:j.prefixLen]) != j.prefix {
		return nil, clientFailure("kiro_tool_session_mismatch", "Kiro 工具续接的模型、提示词、工具或原始会话不匹配")
	}
	// A separate read-only ACP session checks the allowance without interrupting
	// the native prompt which is currently waiting on an MCP response.
	if _, err := j.a.ListModels(ctx); err != nil {
		j.close()
		return nil, err
	}
	results := map[string]*llm.ToolResult{}
	for _, m := range r.Messages[j.prefixLen:] {
		for _, b := range m.Content {
			if b.Type == llm.ContentToolResult && b.ToolResult != nil {
				results[b.ToolResult.ToolCallID] = b.ToolResult
			} else if m.Role == llm.RoleUser {
				return nil, clientFailure("kiro_tool_session_mismatch", "请先回传 Kiro 请求的工具结果，再发送新消息")
			}
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.sink != nil {
		return nil, clientFailure("kiro_tool_session_busy", "Kiro 工具会话已结束或正在续接")
	}
	count := 0
	for _, w := range j.waits {
		if w.delivered && !w.answered {
			if results[w.id] == nil {
				return nil, clientFailure("kiro_tool_result_missing", "缺少 Kiro 请求的工具结果")
			}
			count++
		}
	}
	if count == 0 {
		return nil, clientFailure("kiro_tool_result_missing", "没有待续接的 Kiro 工具调用")
	}
	s := j.attach()
	for _, w := range j.waits {
		if w.delivered && !w.answered {
			w.answered = true
			w.result <- results[w.id]
		}
	}
	j.deliverWaiting()
	j.watch(ctx, s)
	return s, nil
}

func (j *job) notify(f rpcFrame) {
	if f.Method != "session/update" {
		return
	}
	var p struct {
		Session string `json:"sessionId"`
		Update  struct {
			Type    string                      `json:"sessionUpdate"`
			Content struct{ Type, Text string } `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(f.Params, &p) != nil || p.Update.Type != "agent_message_chunk" || p.Update.Content.Type != "text" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.sink == nil || p.Session != j.session {
		return
	}
	if !j.textOpen {
		j.send(llm.ResponseEvent{Type: llm.EventTextStart, ContentIndex: j.index})
		j.textOpen = true
	}
	j.send(llm.ResponseEvent{Type: llm.EventTextDelta, ContentIndex: j.index, Delta: p.Update.Content.Text})
}

func (j *job) close() {
	j.closeOnce.Do(func() {
		j.cancel()
		j.mu.Lock()
		j.closed = true
		waits := append([]*toolWait(nil), j.waits...)
		j.mu.Unlock()
		j.a.mu.Lock()
		for _, w := range waits {
			delete(j.a.pending, w.id)
		}
		j.a.mu.Unlock()
		if j.server != nil {
			_ = j.server.Close()
		}
		if j.c != nil {
			j.c.close()
		}
		removeWorkspace(j.a.cfg.WorkRoot, j.work)
	})
}

func (j *job) serveMCP(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		http.Error(w, "local only", 403)
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(405)
		return
	}
	var frame rpcFrame
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024*1024)).Decode(&frame) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if len(frame.ID) == 0 {
		w.WriteHeader(202)
		return
	}
	respond := func(value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(frame.ID), "result": value})
	}
	fail := func() {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(frame.ID), "error": map[string]any{"code": -32602, "message": "Unsupported caller tool request"}})
	}
	switch frame.Method {
	case "initialize":
		respond(map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "Agent2API", "version": "1"}})
	case "ping":
		respond(map[string]any{})
	case "tools/list":
		tools := []any{}
		for _, t := range j.tools {
			schema := t.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
		}
		respond(map[string]any{"tools": tools})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(frame.Params, &p) != nil || len(p.Arguments) == 0 || !json.Valid(p.Arguments) || strings.TrimSpace(string(p.Arguments))[0] != '{' {
			fail()
			return
		}
		known := false
		for _, t := range j.tools {
			known = known || p.Name == t.Name
		}
		if !known {
			fail()
			return
		}
		t := &toolWait{id: "kiro_call_" + randomID(), name: p.Name, arguments: string(p.Arguments), result: make(chan *llm.ToolResult, 1)}
		j.mu.Lock()
		if j.closed {
			j.mu.Unlock()
			fail()
			return
		}
		j.waits = append(j.waits, t)
		j.a.mu.Lock()
		j.a.pending[t.id] = j
		j.a.mu.Unlock()
		j.deliverWaiting()
		j.mu.Unlock()
		select {
		case result := <-t.result:
			text := toolText(result)
			respond(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": result.IsError})
		case <-j.ctx.Done():
			http.Error(w, "session ended", 410)
		case <-r.Context().Done():
			go j.close()
		}
	default:
		fail()
	}
}

func renderPrompt(r llm.RequestMessages) string {
	// ACP is an agent harness, not a raw chat-completions endpoint. Preserve all
	// roles and tool records in a structured envelope instead of flattening them.
	conversation := []any{}
	for _, m := range r.Messages {
		blocks := []any{}
		for _, b := range m.Content {
			switch b.Type {
			case llm.ContentText:
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case llm.ContentThinking: // Do not re-send private reasoning.
			case llm.ContentToolCall:
				if b.ToolCall != nil {
					blocks = append(blocks, map[string]any{"type": "tool_call", "id": b.ToolCall.ID, "name": b.ToolCall.Name, "arguments": b.ToolCall.Arguments})
				}
			case llm.ContentToolResult:
				if b.ToolResult != nil {
					blocks = append(blocks, map[string]any{"type": "tool_result", "id": b.ToolResult.ToolCallID, "content": toolText(b.ToolResult), "is_error": b.ToolResult.IsError})
				}
			}
		}
		conversation = append(conversation, map[string]any{"role": m.Role, "content": blocks})
	}
	var instruction strings.Builder
	instruction.WriteString("Respond to the last message in the following API conversation. Treat its system instruction as the caller's instruction, and preserve conversational roles. Use only the provided caller tools, including for any file or shell operation in the caller's workspace. Do not access the gateway host or use native host tools. Call at most one tool at a time.\n")
	if r.ToolChoice != nil {
		switch r.ToolChoice.Mode {
		case llm.ToolChoiceRequired:
			instruction.WriteString("You must call a provided tool before answering.\n")
		case llm.ToolChoiceFunction:
			instruction.WriteString("Call the provided tool named " + r.ToolChoice.FunctionName + " before answering.\n")
		}
	}
	system := r.SystemPrompt
	if system == "" {
		var parts []string
		for _, b := range r.SystemBlocks {
			if b.Type == llm.ContentText {
				parts = append(parts, b.Text)
			}
		}
		system = strings.Join(parts, "\n")
	}
	data, _ := json.Marshal(map[string]any{"system": system, "conversation": conversation})
	instruction.Write(data)
	return instruction.String()
}

func toolText(result *llm.ToolResult) string {
	if result.Content != "" {
		return result.Content
	}
	var parts []string
	for _, b := range result.Blocks {
		if b.Type == llm.ContentText {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
