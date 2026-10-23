// Package kiro connects the official Kiro CLI ACP server to the gateway.
package kiro

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

const Platform = "kiro"

type Config struct {
	CLIPath, WorkRoot                                 string
	RequestTimeout, StreamTotalTimeout, ModelCacheTTL time.Duration
}

func ResolveCLI(path string) (string, error) {
	if path != "" {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return filepath.Abs(path)
		}
		return "", clientFailure("kiro_cli_missing", "Kiro 官方 CLI 路径不可用，请检查 cli_path")
	}
	if p, err := exec.LookPath("kiro-cli"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		home, _ := os.UserHomeDir()
		for _, p := range []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Kiro-Cli", "kiro-cli.exe"), filepath.Join(os.Getenv("ProgramFiles"), "Kiro-Cli", "kiro-cli.exe"), filepath.Join(home, ".agent2api", "runtimes", "kiro", "2.33.1", "kiro-cli.exe")} {
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				return p, nil
			}
		}
	}
	return "", clientFailure("kiro_cli_missing", "请先安装官方 Kiro CLI，再添加免费账号")
}

func clientFailure(code, message string) *llm.Failure {
	return &llm.Failure{Code: code, Message: message, ClientFixable: true}
}
func upstreamFailure(code, message string) *llm.Failure {
	return &llm.Failure{Code: code, Message: message, UpstreamFault: true}
}

// Paid API-key fallbacks and caller-controlled shell interpolation are excluded.
func cliCommand(ctx context.Context, exe, cwd string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = cwd
	for _, value := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(value), "KIRO_API_KEY=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	hideWindow(cmd)
	return cmd
}

type rpcReply struct {
	Result json.RawMessage
	Err    error
}
type rpcFrame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}
type rpcClient struct {
	cmd       *exec.Cmd
	in        io.WriteCloser
	writeGate chan struct{}
	mu        sync.Mutex
	pending   map[string]chan rpcReply
	sequence  atomic.Uint64
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	onNotify  func(rpcFrame)
	closeOnce sync.Once
}

func startRPC(ctx context.Context, exe, cwd string, onNotify func(rpcFrame)) (*rpcClient, error) {
	ctx, cancel := context.WithCancel(ctx)
	c := &rpcClient{ctx: ctx, cancel: cancel, writeGate: make(chan struct{}, 1), pending: map[string]chan rpcReply{}, done: make(chan struct{}), onNotify: onNotify}
	c.cmd = cliCommand(ctx, exe, cwd, "acp", "--agent", "agent2api-proxy")
	var err error
	if c.in, err = c.cmd.StdinPipe(); err != nil {
		cancel()
		return nil, err
	}
	out, err := c.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	c.cmd.Stderr = io.Discard // Native CLI diagnostics remain in its private log location.
	if err = c.cmd.Start(); err != nil {
		cancel()
		return nil, upstreamFailure("kiro_cli_start", "Kiro CLI 无法启动")
	}
	go func() {
		defer close(c.done)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			var f rpcFrame
			if json.Unmarshal(scanner.Bytes(), &f) != nil {
				continue
			}
			if f.Method != "" && len(f.ID) > 0 {
				// No file, terminal, authentication, or arbitrary host callbacks.
				_ = c.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(f.ID), "error": map[string]any{"code": -32601, "message": "Host operations are unavailable in this API bridge"}})
				continue
			}
			if f.Method != "" {
				if c.onNotify != nil {
					c.onNotify(f)
				}
				continue
			}
			key := string(f.ID)
			c.mu.Lock()
			ch := c.pending[key]
			delete(c.pending, key)
			c.mu.Unlock()
			if ch != nil {
				var e error
				if len(f.Error) > 0 && string(f.Error) != "null" {
					e = upstreamFailure("kiro_rpc_rejected", "Kiro CLI 拒绝了集成请求；请检测连接或更新 CLI")
				}
				ch <- rpcReply{f.Result, e}
			}
		}
		_ = c.cmd.Wait()
		c.mu.Lock()
		for key, ch := range c.pending {
			ch <- rpcReply{Err: upstreamFailure("kiro_cli_closed", "Kiro CLI 连接已关闭")}
			delete(c.pending, key)
		}
		c.mu.Unlock()
	}()
	return c, nil
}
func (c *rpcClient) write(value any) error {
	return c.writeContext(c.ctx, value)
}

func (c *rpcClient) writeContext(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	select {
	case c.writeGate <- struct{}{}:
		defer func() { <-c.writeGate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Cancelling a queued writer leaves the active frame alone. Once a write
	// begins, interruption may leave a partial JSON frame, so end this RPC
	// connection and release every waiter instead of reusing its stdin.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		c.cancel()
		_ = c.in.Close()
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	_, err = c.in.Write(append(raw, '\n'))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func (c *rpcClient) call(ctx context.Context, method string, params any, out any) error {
	id := c.sequence.Add(1)
	key := fmt.Sprint(id)
	ch := make(chan rpcReply, 1)
	c.mu.Lock()
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
	if err := c.writeContext(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return upstreamFailure("kiro_rpc_write", "Kiro CLI 连接不可写")
	}
	select {
	case r := <-ch:
		if r.Err != nil {
			return r.Err
		}
		if out != nil && json.Unmarshal(r.Result, out) != nil {
			return upstreamFailure("kiro_rpc_invalid", "Kiro CLI 返回了无效的数据")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return upstreamFailure("kiro_cli_closed", "Kiro CLI 连接已关闭")
	case <-c.done:
		return upstreamFailure("kiro_cli_closed", "Kiro CLI 连接已关闭")
	}
}
func (c *rpcClient) close() {
	c.closeOnce.Do(func() {
		_ = c.in.Close()
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			c.cancel()
			select {
			case <-c.done:
			case <-time.After(2 * time.Second):
			}
		}
		c.cancel()
	})
}

func prepareWorkspace(root string) (string, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	path, err := os.MkdirTemp(root, "session-")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(path, ".kiro", "agents")
	if os.MkdirAll(dir, 0700) != nil {
		removeWorkspace(root, path)
		return "", errors.New("无法准备 Kiro 独立工作目录")
	}
	agent := map[string]any{"name": "agent2api-proxy", "description": "Model API bridge", "prompt": "You are answering model API requests. Follow the supplied conversation and respond to its last message. Only call tools provided by the caller, at most one tool at a time. File, shell, repository and other workspace operations must use those caller-provided tools, which execute on the caller's computer. Never access the gateway host or use native host tools, resources or other agents.", "tools": []string{"@agent2api"}, "allowedTools": []string{"@agent2api"}, "resources": []any{}, "mcpServers": map[string]any{}, "includeMcpJson": false, "includePowers": false, "hooks": map[string]any{}}
	raw, _ := json.Marshal(agent)
	if os.WriteFile(filepath.Join(dir, "agent2api-proxy.json"), raw, 0600) != nil {
		removeWorkspace(root, path)
		return "", errors.New("无法准备 Kiro 专用工具配置")
	}
	return path, nil
}

type usageData struct {
	Reset          string            `json:"billingCycleReset"`
	Plan           *string           `json:"planName"`
	Overages       *bool             `json:"overagesEnabled"`
	Enterprise     *bool             `json:"isEnterprise"`
	OverageCapable *bool             `json:"overageCapable"`
	Bonus          []json.RawMessage `json:"bonusCredits"`
	AddOns         []json.RawMessage `json:"addOnCredits"`
	Breakdowns     []struct {
		Resource string   `json:"resourceType"`
		Used     *float64 `json:"used"`
		Limit    *float64 `json:"limit"`
		Overages *float64 `json:"currentOverages"`
		Charges  *float64 `json:"overageCharges"`
		HasLimit *bool    `json:"hasLimit"`
	} `json:"usageBreakdowns"`
}
type usageEnvelope struct {
	Success bool      `json:"success"`
	Data    usageData `json:"data"`
}

func initialize(ctx context.Context, c *rpcClient, cwd string, servers []any) (string, []string, error) {
	var init struct {
		Protocol int `json:"protocolVersion"`
		Caps     struct {
			MCP struct {
				HTTP bool `json:"http"`
			} `json:"mcpCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := c.call(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]any{"name": "Agent2API", "version": "1"}}, &init); err != nil {
		return "", nil, err
	}
	if init.Protocol != 1 || (len(servers) > 0 && !init.Caps.MCP.HTTP) {
		return "", nil, clientFailure("kiro_cli_incompatible", "当前 Kiro CLI 未提供所需的 ACP / MCP HTTP 能力")
	}
	var session struct {
		ID     string `json:"sessionId"`
		Models struct {
			Available []struct {
				ID string `json:"modelId"`
			} `json:"availableModels"`
		} `json:"models"`
		Modes struct {
			Current string `json:"currentModeId"`
		} `json:"modes"`
	}
	if err := c.call(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": servers}, &session); err != nil {
		return "", nil, err
	}
	if session.ID == "" || session.Modes.Current != "agent2api-proxy" {
		return "", nil, clientFailure("kiro_profile_unavailable", "Kiro 未应用本网关的专用工具配置，已停止调用")
	}
	ids := []string{}
	for _, m := range session.Models.Available {
		ids = append(ids, m.ID)
	}
	return session.ID, ids, nil
}
func readUsage(ctx context.Context, c *rpcClient, session string) (usageData, error) {
	var reply usageEnvelope
	err := c.call(ctx, "_kiro.dev/commands/execute", map[string]any{"sessionId": session, "command": map[string]any{"command": "usage", "args": map[string]any{}}}, &reply)
	if err != nil {
		return reply.Data, err
	}
	if !reply.Success {
		return reply.Data, upstreamFailure("kiro_usage_unknown", "无法确认 Kiro 免费套餐与余额")
	}
	return reply.Data, nil
}

func readCLIModels(ctx context.Context, exe, cwd string) ([]byte, error) {
	cmd := cliCommand(ctx, exe, cwd, "chat", "--list-models", "--format", "json")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, clientFailure("kiro_login_required", "请通过官方 Kiro CLI 完成网页登录，再检测连接")
	}
	if stdout.Len() > 2*1024*1024 {
		return nil, upstreamFailure("kiro_catalog_invalid", "Kiro 模型目录过大")
	}
	return stdout.Bytes(), nil
}
