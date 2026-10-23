package adapter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func waitForQueued(t *testing.T, p *Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := p.queued
		p.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queue count did not reach expected value")
}

func queueTestPool(t *testing.T) *Pool {
	t.Helper()
	p := NewPool("test", nil)
	p.SetMaxConcurrencyPerAccount(1)
	p.SetQueueLimits(2, time.Second)
	for _, label := range []string{"first", "second"} {
		p.Add(label, &fakeAd{name: "test", events: []llm.ResponseEvent{{Type: llm.EventStart}}})
	}
	return p
}

func TestQueueUsesWhicheverAccountBecomesAvailable(t *testing.T) {
	p := queueTestPool(t)
	req := llm.RequestMessages{Model: "m"}
	first, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream(first)
	defer closeStream(second)
	execution := llm.NewExecution(6)
	execution.StartRouting(time.Second)
	ctx := llm.WithExecution(context.Background(), execution)
	result := make(chan llm.ResponseStream, 1)
	failed := make(chan error, 1)
	go func() {
		stream, err := p.Stream(ctx, req)
		if err != nil {
			failed <- err
		} else {
			result <- stream
		}
	}()
	waitForQueued(t, p, 1)
	time.Sleep(2 * time.Millisecond)
	if got := execution.Snapshot().Phase; got != "queued" {
		t.Fatalf("phase=%s", got)
	}
	// 关闭任意一个账号的流，排队者必须使用它，而非继续等待另一个账号。
	closeStream(second)
	select {
	case stream := <-result:
		defer closeStream(stream)
		if AccountOf(stream) != AccountOf(second) {
			t.Fatal("did not use released account")
		}
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(300 * time.Millisecond):
		t.Fatal("request remained stuck behind the other account")
	}
	waitForQueued(t, p, 0)
	if execution.Snapshot().QueueMs < 1 {
		t.Fatal("queue duration was not measured")
	}
}

func TestQueueLimitCancellationAndTimeoutDoNotDamageAccounts(t *testing.T) {
	p := queueTestPool(t)
	p.SetQueueLimits(1, time.Second)
	req := llm.RequestMessages{Model: "m"}
	first, _ := p.Stream(context.Background(), req)
	second, _ := p.Stream(context.Background(), req)
	defer closeStream(first)
	defer closeStream(second)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := p.Stream(ctx, req); result <- err }()
	waitForQueued(t, p, 1)
	_, err := p.Stream(context.Background(), req)
	if f := llm.Wrap(err); f.Code != "queue_full" || f.HTTPStatus() != 503 {
		t.Fatalf("overflow=%v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	waitForQueued(t, p, 0)
	p.SetQueueLimits(1, 15*time.Millisecond)
	_, err = p.Stream(context.Background(), req)
	if f := llm.Wrap(err); f.Code != "queue_timeout" || f.HTTPStatus() != 503 {
		t.Fatalf("timeout=%v", err)
	}
	waitForQueued(t, p, 0)
	for _, status := range p.Statuses() {
		if !status.Healthy || status.LastError != "" {
			t.Fatal("local queue failure damaged an account")
		}
	}
	closeStream(first)
	closeStream(second)
	stream, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	closeStream(stream)
}

func TestQueueReservationNeverExceedsAccountLimit(t *testing.T) {
	p := NewPool("test", nil)
	p.SetMaxConcurrencyPerAccount(2)
	p.SetQueueLimits(64, 2*time.Second)
	var active, peak atomic.Int32
	for _, label := range []string{"a", "b"} {
		p.Add(label, &fakeAd{name: "test", events: []llm.ResponseEvent{{Type: llm.EventStart}}, onStream: func() {
			n := active.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
		}, onClose: func() { active.Add(-1) }})
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, err := p.Stream(context.Background(), llm.RequestMessages{Model: "m"})
			if err != nil {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
			closeStream(stream)
		}()
	}
	wg.Wait()
	if peak.Load() > 4 || active.Load() != 0 {
		t.Fatalf("peak=%d active=%d", peak.Load(), active.Load())
	}
	for _, status := range p.Statuses() {
		if status.InFlight != 0 {
			t.Fatal("slot leaked")
		}
	}
	waitForQueued(t, p, 0)
}

func TestChangingConcurrencyKeepsExistingLeasesCounted(t *testing.T) {
	p := NewPool("test", nil)
	p.SetMaxConcurrencyPerAccount(2)
	p.Add("a", &fakeAd{name: "test", events: []llm.ResponseEvent{{Type: llm.EventStart}}})
	req := llm.RequestMessages{Model: "m"}
	first, _ := p.Stream(context.Background(), req)
	second, _ := p.Stream(context.Background(), req)
	defer closeStream(first)
	defer closeStream(second)
	p.SetMaxConcurrencyPerAccount(1)
	closeStream(first)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if stream, err := p.Stream(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		if stream != nil {
			closeStream(stream)
		}
		t.Fatalf("old lease bypassed the reduced limit: %v", err)
	}
	closeStream(second)
	stream, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	closeStream(stream)
	if p.Statuses()[0].InFlight != 0 {
		t.Fatal("old semaphore lease leaked after reconfiguration")
	}
}

func TestQueuedRequestWakesWhenAnotherAccountCooldownExpires(t *testing.T) {
	p := queueTestPool(t)
	_, _, release, err := p.reserve(context.Background(), "m", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p.cooldownModel(p.accounts[1], "m", time.Hour, "test reset")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		stream, err := p.Stream(ctx, llm.RequestMessages{Model: "m"})
		if err == nil {
			if AccountOf(stream) != "second" {
				err = errors.New("used the still-full account")
			}
			closeStream(stream)
		}
		done <- err
	}()
	waitForQueued(t, p, 1)
	// Publish a nearby known reset after the caller has entered the queue.
	p.mu.Lock()
	p.accounts[1].modelCooldown["m"] = time.Now().Add(40 * time.Millisecond)
	p.notifyLocked()
	p.mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("idle account recovered but request still timed out: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue did not notice the cooldown expiring without another release")
	}
}
