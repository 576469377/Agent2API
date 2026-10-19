package cursor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

const maxFrame = 8 * 1024 * 1024

func writeFrame(w io.Writer, message any) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(raw) > maxFrame {
		return fmt.Errorf("Cursor request frame too large")
	}
	frame := make([]byte, 5+len(raw))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(raw)))
	copy(frame[5:], raw)
	_, err = w.Write(frame)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	flag, n := header[0], binary.BigEndian.Uint32(header[1:])
	if flag & ^byte(3) != 0 || n > maxFrame {
		return 0, nil, fmt.Errorf("invalid Cursor frame")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return 0, nil, err
	}
	if flag&1 != 0 {
		z, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return 0, nil, err
		}
		raw, err = io.ReadAll(io.LimitReader(z, maxFrame+1))
		z.Close()
		if err != nil || len(raw) > maxFrame {
			return 0, nil, fmt.Errorf("invalid compressed Cursor frame")
		}
	}
	return flag, raw, nil
}

type wireInt int

func (n *wireInt) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(string(raw), "\"")
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return fmt.Errorf("invalid token count")
	}
	*n = wireInt(v)
	return nil
}

type turnUsage struct {
	Input     wireInt `json:"inputTokens"`
	Output    wireInt `json:"outputTokens"`
	Cached    wireInt `json:"cacheReadTokens"`
	Reasoning wireInt `json:"reasoningTokens"`
}
type serverMessage struct {
	Interaction *struct {
		Text *struct {
			Text   string `json:"text"`
			Notice bool   `json:"isServerNotice"`
		} `json:"textDelta"`
		Thinking *struct {
			Text string `json:"text"`
		} `json:"thinkingDelta"`
		ThinkingCompleted json.RawMessage `json:"thinkingCompleted"`
		Tokens            *struct {
			Tokens wireInt `json:"tokens"`
		} `json:"tokenDelta"`
		Ended *turnUsage `json:"turnEnded"`
	} `json:"interactionUpdate"`
	KV *struct {
		ID  int `json:"id"`
		Set *struct {
			ID   string `json:"blobId"`
			Data string `json:"blobData"`
		} `json:"setBlobArgs"`
		Get *struct {
			ID string `json:"blobId"`
		} `json:"getBlobArgs"`
	} `json:"kvServerMessage"`
	Exec  map[string]json.RawMessage `json:"execServerMessage"`
	Query json.RawMessage            `json:"interactionQuery"`
}

type upstreamStream struct {
	body         io.ReadCloser
	writer       *io.PipeWriter
	cancel       context.CancelFunc
	ctx          context.Context
	events       chan llm.ResponseEvent
	err          error
	errReturned  bool
	once         sync.Once
	idle         *time.Timer
	idleDuration time.Duration
	timedOut     atomic.Bool
	req          llm.RequestMessages
	tools        []map[string]any
	blobs        map[string]string
	blobBytes    int
	block        llm.ResponseEventType
	index        int
	started      bool
	outputTokens int
}

func newStream(parent context.Context, body io.ReadCloser, writer *io.PipeWriter, cancel context.CancelFunc, req llm.RequestMessages, tools []map[string]any, blobs map[string]string, idle time.Duration) *upstreamStream {
	ctx, stop := context.WithCancel(parent)
	s := &upstreamStream{body: body, writer: writer, cancel: func() { stop(); cancel() }, ctx: ctx, req: req, tools: tools, blobs: blobs, idleDuration: idle, events: make(chan llm.ResponseEvent, 32)}
	for _, v := range blobs {
		s.blobBytes += len(v)
	}
	s.idle = time.NewTimer(idle)
	go func() {
		select {
		case <-s.idle.C:
			s.timedOut.Store(true)
			s.Close()
		case <-ctx.Done():
		}
	}()
	go func() { defer close(s.events); defer s.Close(); s.err = s.pump() }()
	return s
}

func (s *upstreamStream) AccountLabel() string { return "Cursor Free" }
func (s *upstreamStream) Close() error {
	s.once.Do(func() { s.idle.Stop(); s.cancel(); s.writer.Close(); s.body.Close() })
	return nil
}
func (s *upstreamStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	select {
	case <-ctx.Done():
		s.Close()
		return llm.ResponseEvent{}, ctx.Err()
	case event, ok := <-s.events:
		if ok {
			return event, nil
		}
		if s.err != nil && !s.errReturned {
			s.errReturned = true
			return llm.ResponseEvent{}, s.err
		}
		return llm.ResponseEvent{}, llm.ErrStreamDone
	}
}
func (s *upstreamStream) emit(ev llm.ResponseEvent) error {
	select {
	case s.events <- ev:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *upstreamStream) start() error {
	if s.started {
		return nil
	}
	s.started = true
	return s.emit(llm.ResponseEvent{Type: llm.EventStart, Model: verifiedModel, ResponseID: newID()})
}
func (s *upstreamStream) closeBlock() error {
	var event llm.ResponseEventType
	switch s.block {
	case llm.EventTextDelta:
		event = llm.EventTextEnd
	case llm.EventThinkingDelta:
		event = llm.EventThinkingEnd
	default:
		return nil
	}
	s.block = ""
	err := s.emit(llm.ResponseEvent{Type: event, ContentIndex: s.index})
	s.index++
	return err
}
func (s *upstreamStream) delta(kind llm.ResponseEventType, value string) error {
	if value == "" {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	if s.block != kind {
		if err := s.closeBlock(); err != nil {
			return err
		}
		s.block = kind
		start := llm.EventTextStart
		if kind == llm.EventThinkingDelta {
			start = llm.EventThinkingStart
		}
		if err := s.emit(llm.ResponseEvent{Type: start, ContentIndex: s.index}); err != nil {
			return err
		}
	}
	return s.emit(llm.ResponseEvent{Type: kind, ContentIndex: s.index, Delta: value})
}
func (s *upstreamStream) finish(reason llm.StopReason, usage *llm.Usage) error {
	if err := s.start(); err != nil {
		return err
	}
	if err := s.closeBlock(); err != nil {
		return err
	}
	return s.emit(llm.ResponseEvent{Type: llm.EventDone, StopReason: reason, Usage: usage})
}

func (s *upstreamStream) pump() error {
	for {
		flags, raw, err := readFrame(s.body)
		if err != nil {
			if s.timedOut.Load() {
				return &llm.Failure{Code: "cursor_idle_timeout", Message: "Cursor 流响应超时", Timeout: true, Cause: context.DeadlineExceeded}
			}
			if err := s.ctx.Err(); err != nil {
				return llm.Wrap(err)
			}
			return fault("cursor_incomplete_stream", "Cursor 流在 turnEnded 前中断")
		}
		s.idle.Reset(s.idleDuration)
		if flags&2 != 0 {
			var trailer struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(raw, &trailer) != nil {
				return fault("cursor_invalid_trailer", "Cursor Connect 结束帧无效")
			}
			if len(trailer.Error) > 0 && string(trailer.Error) != "null" {
				return connectFailure(trailer.Error)
			}
			return fault("cursor_missing_turn_end", "Cursor 流缺少 turnEnded，未确认成功")
		}
		var msg serverMessage
		if json.Unmarshal(raw, &msg) != nil {
			return fault("cursor_invalid_frame", "Cursor Connect 消息无效")
		}
		if msg.KV != nil {
			kv := msg.KV
			reply := map[string]any{"id": kv.ID}
			switch {
			case kv.Set != nil:
				s.blobBytes += len(kv.Set.Data) - len(s.blobs[kv.Set.ID])
				if s.blobBytes > 64*1024*1024 {
					return fault("cursor_blob_limit", "Cursor 会话临时数据超限")
				}
				s.blobs[kv.Set.ID] = kv.Set.Data
				reply["setBlobResult"] = map[string]any{}
			case kv.Get != nil:
				v, ok := s.blobs[kv.Get.ID]
				if !ok {
					return fault("cursor_missing_blob", "Cursor 请求了不存在的会话数据")
				}
				reply["getBlobResult"] = map[string]any{"blobData": v}
			default:
				return fault("cursor_invalid_kv", "Cursor 会话数据请求无效")
			}
			if err := writeFrame(s.writer, map[string]any{"kvClientMessage": reply}); err != nil {
				return transportError(err)
			}
		}
		if len(msg.Exec) > 0 {
			done, err := s.exec(msg.Exec)
			if err != nil || done {
				return err
			}
		}
		if len(msg.Query) > 0 && string(msg.Query) != "null" {
			return requestError("Cursor 请求交互确认，普通 API 反代无法处理")
		}
		if u := msg.Interaction; u != nil {
			if u.Text != nil {
				if u.Text.Notice {
					return fault("cursor_server_notice", "Cursor 返回服务提示，未确认为模型回复")
				}
				if err := s.delta(llm.EventTextDelta, u.Text.Text); err != nil {
					return err
				}
			}
			if u.Thinking != nil {
				if err := s.delta(llm.EventThinkingDelta, u.Thinking.Text); err != nil {
					return err
				}
			}
			if len(u.ThinkingCompleted) > 0 && s.block == llm.EventThinkingDelta {
				if err := s.closeBlock(); err != nil {
					return err
				}
			}
			if u.Tokens != nil {
				s.outputTokens += int(u.Tokens.Tokens)
			}
			if u.Ended != nil {
				if s.req.ToolChoice != nil && (s.req.ToolChoice.Mode == llm.ToolChoiceRequired || s.req.ToolChoice.Mode == llm.ToolChoiceFunction) {
					return requestError("Cursor 本轮未调用要求的客户端工具")
				}
				u := u.Ended
				return s.finish(llm.StopReasonStop, &llm.Usage{InputTokens: int(u.Input), OutputTokens: int(u.Output), CachedTokens: int(u.Cached), ReasoningTokens: int(u.Reasoning), TotalTokens: int(u.Input + u.Output)})
			}
			limit := 8192
			if s.req.MaxTokens != nil {
				limit = min(*s.req.MaxTokens, 8192)
			}
			if s.outputTokens >= limit {
				return s.finish(llm.StopReasonLength, &llm.Usage{OutputTokens: s.outputTokens, TotalTokens: s.outputTokens})
			}
		}
	}
}

func (s *upstreamStream) exec(e map[string]json.RawMessage) (bool, error) {
	var id int
	var execID string
	_ = json.Unmarshal(e["id"], &id)
	_ = json.Unmarshal(e["execId"], &execID)
	if _, ok := e["mcpStateExecArgs"]; ok {
		// Resolve only schemas supplied in this API request. No MCP connections.
		servers := []map[string]any{}
		if len(s.tools) > 0 {
			servers = append(servers, map[string]any{"serverName": "agent2api", "serverIdentifier": "agent2api", "status": "connected", "tools": s.tools})
		}
		return false, writeFrame(s.writer, map[string]any{"execClientMessage": map[string]any{"id": id, "execId": execID, "mcpStateExecResult": map[string]any{"success": map[string]any{"servers": servers}}}})
	}
	if raw, ok := e["mcpArgs"]; ok {
		var call struct {
			Name     string          `json:"toolName"`
			Provider string          `json:"providerIdentifier"`
			ID       string          `json:"toolCallId"`
			Args     json.RawMessage `json:"args"`
		}
		if json.Unmarshal(raw, &call) != nil || call.Provider != "agent2api" {
			return false, requestError("Cursor 请求了未声明的工具")
		}
		found := false
		for _, t := range s.tools {
			if t["toolName"] == call.Name {
				found = true
				break
			}
		}
		if !found {
			return false, requestError("Cursor 请求了未声明的工具")
		}
		args := call.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if len(args) == 0 || args[0] != '{' || !json.Valid(args) {
			return false, requestError("Cursor 工具参数必须为 JSON 对象")
		}
		return s.relayTool(call.Name, call.ID, args)
	}
	if raw, ok := e["readArgs"]; ok {
		// Composer can select its native Read even when the client supplies an
		// MCP Read. Translate only this known, equivalent schema and return the
		// call to the client; never execute the host-side operation.
		var read struct {
			Path     string `json:"path"`
			ID       string `json:"toolCallId"`
			Offset   *int   `json:"offset"`
			Limit    *int   `json:"limit"`
			Encoding string `json:"encodingHint"`
			Digest   bool   `json:"digestOnly"`
		}
		if json.Unmarshal(raw, &read) == nil && read.Path != "" && read.Encoding == "" && !read.Digest && (read.Offset == nil || *read.Offset >= 0) && (read.Limit == nil || *read.Limit > 0) {
			args := map[string]any{"file_path": read.Path}
			if read.Offset != nil {
				args["offset"] = max(1, *read.Offset) // Both tools use 1-indexed lines.
			}
			if read.Limit != nil {
				args["limit"] = *read.Limit
			}
			for _, tool := range s.req.Tools {
				if tool.Name == "Read" && s.hasTool("Read") && acceptsRead(tool.Parameters, args) {
					encoded, _ := json.Marshal(args)
					return s.relayTool("Read", read.ID, encoded)
				}
			}
		}
	}
	// Reject host-side shell/files/browser/other actions; the gateway never runs
	// the desktop agent's tools or invents a successful result for them.
	operation := ""
	for _, name := range []string{"readArgs", "shellArgs", "writeArgs", "deleteArgs", "listDirArgs", "grepArgs"} {
		if _, ok := e[name]; ok {
			operation = " (" + name + ")"
			break
		}
	}
	return false, requestError("Cursor 请求了主机工具" + operation + "，网关仅转发本次请求声明的客户端工具")
}

func (s *upstreamStream) hasTool(name string) bool {
	for _, tool := range s.tools {
		if tool["toolName"] == name {
			return true
		}
	}
	return false
}

func acceptsRead(schema map[string]any, args map[string]any) bool {
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	for name := range args {
		p, ok := properties[name].(map[string]any)
		if !ok || name == "file_path" && p["type"] != "string" || name != "file_path" && p["type"] != "number" && p["type"] != "integer" {
			return false
		}
	}
	var required []string
	switch values := schema["required"].(type) {
	case []string:
		required = values
	case []any:
		for _, name := range values {
			required = append(required, fmt.Sprint(name))
		}
	}
	for _, name := range required {
		if _, exists := args[name]; !exists {
			return false
		}
	}
	return true
}

func (s *upstreamStream) relayTool(name, id string, args json.RawMessage) (bool, error) {
	if id == "" {
		id = newID()
	}
	if err := s.start(); err != nil {
		return false, err
	}
	if err := s.closeBlock(); err != nil {
		return false, err
	}
	for _, ev := range []llm.ResponseEvent{
		{Type: llm.EventToolCallStart, ContentIndex: s.index, ToolCallID: id, ToolName: name},
		{Type: llm.EventToolCallDelta, ContentIndex: s.index, Delta: string(args)},
		{Type: llm.EventToolCallEnd, ContentIndex: s.index, ToolCallID: id, ToolName: name, ToolArguments: string(args), ToolArgumentsDone: true},
	} {
		if err := s.emit(ev); err != nil {
			return false, err
		}
	}
	return true, s.finish(llm.StopReasonToolCalls, &llm.Usage{OutputTokens: s.outputTokens, TotalTokens: s.outputTokens})
}

func connectFailure(raw json.RawMessage) *llm.Failure {
	var e struct {
		Code    string `json:"code"`
		Details []struct {
			Debug struct {
				Error string `json:"error"`
			} `json:"debug"`
		} `json:"details"`
	}
	_ = json.Unmarshal(raw, &e)
	f := fault("cursor_generation_failed", "Cursor 生成被上游拒绝")
	for _, d := range e.Details {
		code := strings.ToUpper(d.Debug.Error)
		if strings.Contains(code, "USAGE_LIMIT") || strings.Contains(code, "QUOTA") || strings.Contains(code, "RATE_LIMIT") || strings.Contains(code, "FREE_TIER_LIMIT") {
			f.UpstreamFault = false
			f.RateLimited = true
			f.Message = "Cursor 免费额度已用尽或暂时限流；请等待额度恢复"
			return f
		}
		if code == "ERROR_OUTDATED_CLIENT" {
			f.UpstreamFault = false
			f.ClientFixable = true
			f.Message = "Cursor 拒绝当前客户端配置，请升级并检查原有设备标识"
			return f
		}
	}
	switch e.Code {
	case "unauthenticated":
		f.UpstreamFault = false
		f.Unauthorized = true
	case "permission_denied", "invalid_argument", "failed_precondition":
		f.UpstreamFault = false
		f.ClientFixable = true
	case "resource_exhausted":
		f.UpstreamFault = false
		f.RateLimited = true
	}
	return f
}
