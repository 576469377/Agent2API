package llm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type executionKey struct{}
type attemptAccountKey struct{}
type attemptIndexKey struct{}

type Attempt struct {
	Number     int    `json:"number"`
	Platform   string `json:"platform"`
	AccountID  string `json:"account_id,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Status     int    `json:"status,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
}

type ExecutionSnapshot struct {
	RequestID    string    `json:"request_id"`
	Phase        string    `json:"phase"`
	AttemptCount int       `json:"attempt_count"`
	QueueMs      int64     `json:"queue_ms"`
	Attempts     []Attempt `json:"attempts,omitempty"`
}

// Execution 只保留调度计数和耗时，不保留会话键、凭证、提示或输出正文。
type Execution struct {
	mu            sync.Mutex
	id, phase     string
	maxAttempts   int
	deadline      time.Time
	queueStarted  time.Time
	queueDuration time.Duration
	attempts      []Attempt
}

func NewExecution(maxAttempts int) *Execution {
	if maxAttempts <= 0 {
		maxAttempts = 6
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	return &Execution{id: hex.EncodeToString(id[:]), phase: "reading", maxAttempts: maxAttempts}
}

func WithExecution(ctx context.Context, execution *Execution) context.Context {
	return context.WithValue(ctx, executionKey{}, execution)
}
func ExecutionFrom(ctx context.Context) *Execution {
	execution, _ := ctx.Value(executionKey{}).(*Execution)
	return execution
}

func WithAttemptAccount(ctx context.Context, platform, label string) context.Context {
	digest := sha256.Sum256([]byte(platform + "\x00" + label))
	return context.WithValue(ctx, attemptAccountKey{}, hex.EncodeToString(digest[:8]))
}

func (e *Execution) StartRouting(timeout time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.phase = "routing"
	if timeout > 0 {
		e.deadline = time.Now().Add(timeout)
	}
}

func (e *Execution) SetPhase(phase string) { e.mu.Lock(); e.phase = phase; e.mu.Unlock() }

// SetAttemptPhase 忽略已完成或旧尝试的迟到回调，避免双向流上传结束时覆盖接收状态。
func SetAttemptPhase(ctx context.Context, phase string) {
	e := ExecutionFrom(ctx)
	index, ok := ctx.Value(attemptIndexKey{}).(int)
	if e == nil || !ok || index < 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if index != len(e.attempts)-1 || e.attempts[index].Status != 0 || e.attempts[index].ErrorCode != "" {
		return
	}
	if e.phase == "connecting" || e.phase == "uploading" || e.phase == "waiting_headers" {
		e.phase = phase
	}
}

func (e *Execution) Remaining() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.deadline.IsZero() {
		return 24 * time.Hour
	}
	return time.Until(e.deadline)
}

func (e *Execution) Snapshot() ExecutionSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	queue := e.queueDuration
	if !e.queueStarted.IsZero() {
		queue += time.Since(e.queueStarted)
	}
	return ExecutionSnapshot{e.id, e.phase, len(e.attempts), queue.Milliseconds(), append([]Attempt(nil), e.attempts...)}
}

func BeginQueue(ctx context.Context) func() {
	e := ExecutionFrom(ctx)
	if e == nil {
		return func() {}
	}
	e.mu.Lock()
	e.phase = "queued"
	e.queueStarted = time.Now()
	e.mu.Unlock()
	return sync.OnceFunc(func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.queueDuration += time.Since(e.queueStarted)
		e.queueStarted = time.Time{}
		e.phase = "routing"
	})
}

func NoteAttemptFailure(ctx context.Context, err error) {
	if e := ExecutionFrom(ctx); e != nil && err != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.attempts) > 0 {
			e.attempts[len(e.attempts)-1].ErrorCode = Wrap(err).Code
		}
		e.phase = "routing"
	}
}

// BeginAttempt 在真正发送生成请求时计数，配置查询和凭证刷新不消耗尝试数。
// finish 只停止等待响应头的计时器，成功后不会截断后续长流。
func BeginAttempt(ctx context.Context, platform string, headerTimeout time.Duration) (context.Context, func(int, error) error, error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	e := ExecutionFrom(ctx)
	index := -1
	if e != nil {
		e.mu.Lock()
		if len(e.attempts) >= e.maxAttempts {
			e.mu.Unlock()
			return ctx, nil, &Failure{Code: "retry_budget_exhausted", Message: "本次请求已达到上游尝试上限，请稍后再试", Local: true}
		}
		if !e.deadline.IsZero() {
			remaining := time.Until(e.deadline)
			if remaining <= 0 {
				e.mu.Unlock()
				return ctx, nil, &Failure{Code: "routing_timeout", Message: "本次请求等待上游的总时间已用尽，请稍后再试", Local: true}
			}
			if headerTimeout > 0 && remaining < headerTimeout {
				headerTimeout = remaining
			}
		}
		account, _ := ctx.Value(attemptAccountKey{}).(string)
		index = len(e.attempts)
		e.attempts = append(e.attempts, Attempt{Number: index + 1, Platform: platform, AccountID: account})
		e.phase = "connecting"
		e.mu.Unlock()
	}
	started := time.Now()
	child, cancel := context.WithCancel(ctx)
	child = context.WithValue(child, attemptIndexKey{}, index)
	var timer *time.Timer
	expired := make(chan struct{})
	if headerTimeout > 0 {
		timer = time.AfterFunc(headerTimeout, func() { cancel(); close(expired) })
	}
	var once sync.Once
	var result error
	finish := func(status int, err error) error {
		once.Do(func() {
			result = err
			if timer != nil && !timer.Stop() {
				<-expired
				result = &Failure{Code: "upstream_header_timeout", Message: "等待上游响应超时", Cause: context.DeadlineExceeded, Timeout: true, UpstreamFault: true}
			}
			if ctx.Err() != nil {
				// 保留网关主动超时的原因，不能把它记为客户端断开。
				result = context.Cause(ctx)
			}
			if result != nil {
				cancel()
			}
			if e != nil {
				e.mu.Lock()
				defer e.mu.Unlock()
				e.attempts[index].DurationMs = time.Since(started).Milliseconds()
				if status == 0 && result != nil {
					status = Wrap(result).HTTPStatus()
				}
				e.attempts[index].Status = status
				if result != nil {
					e.attempts[index].ErrorCode = Wrap(result).Code
					e.phase = "routing"
				} else {
					e.phase = "waiting_output"
				}
			}
		})
		return result
	}
	return child, finish, nil
}
