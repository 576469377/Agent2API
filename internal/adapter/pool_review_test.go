package adapter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestBoundAccountUsesSameFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failure      *llm.Failure
		wantState    string
		wantFallback bool
	}{
		{"limited", withRetryAfter(rateLimitedFailure("too many requests"), 60), "ready", true},
		{"unauthorized", llm.NewFailure("unauthorized", "please login", nil), "blocked", true},
		{"bad request", &llm.Failure{Code: "invalid_request", Message: "invalid parameter", ClientFixable: true}, "ready", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			badHits, goodHits := 0, 0
			p := NewPool("test", nil)
			p.Add("bound", &fakeAd{name: "test", err: tc.failure, onStream: func() { badHits++ }})
			p.Add("other", &fakeAd{name: "test", onStream: func() { goodHits++ }})
			p.affinity.set("session", "bound")
			s, err := p.Stream(context.Background(), llm.RequestMessages{Model: "m", SessionKey: "session"})
			if tc.wantFallback {
				if err != nil || AccountOf(s) != "other" {
					t.Fatalf("fallback: stream=%v err=%v", s, err)
				}
				_ = closeStream(s)
			} else if !errors.Is(err, tc.failure) || goodHits != 0 {
				t.Fatalf("invalid requests must stop: err=%v other attempts=%d", err, goodHits)
			}
			if badHits != 1 {
				t.Fatalf("bound attempts=%d", badHits)
			}
			status := p.Statuses()[0]
			if status.State != tc.wantState {
				t.Fatalf("state=%s want %s", status.State, tc.wantState)
			}
			if tc.name == "limited" && status.ModelCooldowns["m"] <= 0 {
				t.Fatal("bound model was not cooled")
			}
		})
	}
}

func TestBusyAffinityCanUseAnIdleAccount(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("bound", &fakeAd{name: "test", events: []llm.ResponseEvent{{Type: llm.EventTextDelta, Delta: "first"}}})
	p.Add("idle", &fakeAd{name: "test"})
	p.SetMaxConcurrencyPerAccount(1)
	p.affinity.set("session", "bound")
	first, err := p.Stream(context.Background(), llm.RequestMessages{Model: "m", SessionKey: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream(first)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	next, err := p.Stream(ctx, llm.RequestMessages{Model: "m", SessionKey: "session"})
	if err != nil {
		t.Fatalf("idle capacity must be usable: %v", err)
	}
	defer closeStream(next)
	if AccountOf(next) != "idle" {
		t.Fatalf("selected %s", AccountOf(next))
	}
}

func TestPoolDoesNotAttemptCanceledRequests(t *testing.T) {
	for _, bound := range []bool{false, true} {
		p := NewPool("test", nil)
		hits := 0
		p.Add("a", &fakeAd{name: "test", onStream: func() { hits++ }})
		if bound {
			p.affinity.set("session", "a")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := p.Stream(ctx, llm.RequestMessages{SessionKey: "session"})
		if !errors.Is(err, context.Canceled) || hits != 0 {
			t.Fatalf("bound=%v err=%v attempts=%d", bound, err, hits)
		}
	}
}

func TestCoolingDeadlineIsForRequestedModel(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test"})
	p.Add("b", &fakeAd{name: "test"})
	now := time.Now()
	p.accounts[0].cooldownUntil = now.Add(30 * time.Second)
	p.accounts[0].modelCooldown["m"] = now.Add(90 * time.Second)
	p.accounts[0].modelCooldown["unrelated"] = now.Add(time.Second)
	p.accounts[1].cooldownUntil = now.Add(180 * time.Second)
	p.accounts[1].modelCooldown["m"] = now.Add(60 * time.Second)
	_, err := p.Stream(context.Background(), llm.RequestMessages{Model: "m"})
	f := llm.Wrap(err)
	if !f.RateLimited || f.RetryAfterSeconds < 85 || f.RetryAfterSeconds > 90 {
		t.Fatalf("earliest usable account for m should thaw in 90s: %+v", f)
	}
}

func TestDisabledAccountsAreNotReportedAsRateLimited(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		p := NewPool("test", nil)
		p.Add("disabled", &fakeAd{name: "test"})
		p.accounts[0].state = stateDisabled
		if blocked {
			p.Add("blocked", &fakeAd{name: "test"})
			p.accounts[1].state = stateBlocked
		}
		_, err := p.Stream(context.Background(), llm.RequestMessages{Model: "m"})
		f := llm.Wrap(err)
		want := 503
		if blocked {
			want = 401
		}
		if f.RateLimited || f.HTTPStatus() != want {
			t.Fatalf("blocked=%v failure=%+v", blocked, f)
		}
	}
}

func TestPoolExhaustionWhileCooldownsChange(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("a", &fakeAd{name: "test"})
	p.cooldownModel(p.accounts[0], "m", time.Hour, "limited")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			p.cooldownModel(p.accounts[0], "unrelated", time.Hour, "limited")
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for i := 0; i < 200; i++ {
		_, err := p.Stream(context.Background(), llm.RequestMessages{Model: "m"})
		if err == nil || !llm.Wrap(err).RateLimited {
			t.Fatalf("expected cooling, got %v", err)
		}
	}
}

func TestUpstreamCooldownDoesNotOverflow(t *testing.T) {
	if got := upstreamCooldown(&llm.Failure{RetryAfterSeconds: int(^uint(0) >> 1)}); got != maxCooldown {
		t.Fatalf("large upstream delay must remain bounded: %v", got)
	}
}
