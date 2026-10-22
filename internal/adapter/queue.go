package adapter

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func (p *Pool) notifyLocked() {
	if p.changed != nil {
		close(p.changed)
	}
	p.changed = make(chan struct{})
}

func (p *Pool) SetQueueLimits(maxWaiting int, timeout time.Duration) {
	if maxWaiting <= 0 {
		maxWaiting = 128
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queueLimit, p.queueTimeout = maxWaiting, timeout
	p.notifyLocked()
}

// reserve 在同一临界区选号和占槽；等待的是整个池，而不是某个已满账号。
// 任意账号释放容量时都会重新选号，取消和排队超时不会记为上游故障。
func (p *Pool) reserve(ctx context.Context, model string, tried map[int]bool, bound int) (int, *poolAccount, func(), error) {
	queued := false
	var stopWaiting func()
	var timer *time.Timer
	var timeout <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
		if stopWaiting != nil {
			stopWaiting()
		}
		if queued {
			p.mu.Lock()
			p.queued--
			p.mu.Unlock()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return -1, nil, nil, err
		}
		p.mu.Lock()
		now := time.Now()
		idx := bound
		var acc *poolAccount
		if idx >= 0 && idx < len(p.accounts) && !tried[idx] && p.accounts[idx].available(now, model) && p.accounts[idx].hasCapacity() {
			acc = p.accounts[idx]
		} else {
			idx, acc = p.pickByHealthLocked(now, model, tried)
		}
		if acc == nil {
			p.mu.Unlock()
			return -1, nil, nil, nil
		}
		if acc.hasCapacity() {
			sem := acc.sem
			if sem != nil {
				sem <- struct{}{}
			}
			atomic.AddInt32(&acc.inFlight, 1)
			p.mu.Unlock()
			release := sync.OnceFunc(func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				if sem != nil {
					<-sem
				}
				atomic.AddInt32(&acc.inFlight, -1)
				p.notifyLocked()
			})
			return idx, acc, release, nil
		}
		if !queued {
			limit := p.queueLimit
			if limit <= 0 {
				limit = 128
			}
			if p.queued >= limit {
				p.mu.Unlock()
				return -1, nil, nil, &llm.Failure{Code: "queue_full", Message: "本地等待队列已满，请稍后重试", Local: true, RetryAfterSeconds: 1}
			}
			wait := p.queueTimeout
			if wait <= 0 {
				wait = 30 * time.Second
			}
			if execution := llm.ExecutionFrom(ctx); execution != nil {
				if remaining := execution.Remaining(); remaining < wait {
					wait = remaining
				}
			}
			if wait <= 0 {
				p.mu.Unlock()
				return -1, nil, nil, &llm.Failure{Code: "routing_timeout", Message: "本次请求等待上游的总时间已用尽，请稍后再试", Local: true}
			}
			p.queued++
			queued = true
			stopWaiting = llm.BeginQueue(ctx)
			timer = time.NewTimer(wait)
			timeout = timer.C
		}
		if p.changed == nil {
			p.changed = make(chan struct{})
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return -1, nil, nil, ctx.Err()
		case <-timeout:
			return -1, nil, nil, &llm.Failure{Code: "queue_timeout", Message: "等待可用账号超时，请稍后重试", Local: true, RetryAfterSeconds: 1}
		case <-changed:
		}
	}
}
