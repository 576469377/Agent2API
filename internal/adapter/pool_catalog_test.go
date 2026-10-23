package adapter

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type catalogPoolFixture struct {
	fakeAd
	list func(context.Context) ([]ModelInfo, error)
}

func (f *catalogPoolFixture) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return f.list(ctx)
}

func TestPoolListModelsFallsBackWithoutChangingChatState(t *testing.T) {
	p := NewPool("test", nil)
	p.SetMaxConcurrencyPerAccount(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var calls []string
	p.Add("healthy", &catalogPoolFixture{list: func(got context.Context) ([]ModelInfo, error) {
		if got != ctx {
			t.Fatal("fallback replaced the shared request context")
		}
		calls = append(calls, "healthy")
		return []ModelInfo{{ID: "current-model"}}, nil
	}})
	p.Add("preferred", &catalogPoolFixture{list: func(got context.Context) ([]ModelInfo, error) {
		if got != ctx {
			t.Fatal("first attempt replaced the shared request context")
		}
		calls = append(calls, "preferred")
		return nil, errors.New("configuration request failed")
	}})
	p.accounts[1].samples, p.accounts[1].successEWMA = 20, 1
	before := p.Statuses()
	models, err := p.ListModels(ctx)
	if err != nil || len(models) != 1 || models[0].ID != "current-model" {
		t.Fatalf("healthy fallback was not used: models=%v err=%v calls=%v", models, err, calls)
	}
	if !reflect.DeepEqual(calls, []string{"preferred", "healthy"}) {
		t.Fatalf("candidate order=%v", calls)
	}
	if after := p.Statuses(); !reflect.DeepEqual(before, after) {
		t.Fatalf("catalogue request changed chat scheduling state: before=%+v after=%+v", before, after)
	}
	for _, acc := range p.accounts {
		if len(acc.sem) != 0 {
			t.Fatal("catalogue request reserved a chat slot")
		}
	}
}

func TestPoolListModelsSkipsUnavailableAccounts(t *testing.T) {
	for _, state := range []accountState{stateDisabled, stateBlocked, stateCooldown} {
		t.Run(state.String(), func(t *testing.T) {
			p := NewPool("test", nil)
			calls := 0
			p.Add("unavailable", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) {
				calls++
				return []ModelInfo{{ID: "cached-model"}}, nil
			}})
			p.accounts[0].state = state
			if state == stateCooldown {
				p.accounts[0].cooldownUntil = time.Now().Add(time.Minute)
			}
			models, err := p.ListModels(context.Background())
			if calls != 0 || err == nil || len(models) != 0 {
				t.Fatalf("unavailable account was queried: calls=%d models=%v err=%v", calls, models, err)
			}
		})
	}
}

func TestPoolListModelsStopsOnCancellation(t *testing.T) {
	for _, cancelBefore := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_first_attempt", false: "before_fallback"}[cancelBefore], func(t *testing.T) {
			p := NewPool("test", nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			firstCalls, secondCalls := 0, 0
			p.Add("first", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) {
				firstCalls++
				cancel()
				return nil, errors.New("request failed while caller canceled")
			}})
			p.Add("second", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) {
				secondCalls++
				return []ModelInfo{{ID: "cached-model"}}, nil
			}})
			if cancelBefore {
				cancel()
			}
			_, err := p.ListModels(ctx)
			if !errors.Is(err, context.Canceled) || secondCalls != 0 || cancelBefore && firstCalls != 0 {
				t.Fatalf("canceled request continued: first=%d second=%d err=%v", firstCalls, secondCalls, err)
			}
		})
	}
}

func TestPoolListModelsKeepsSuccessfulEmptyCatalog(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("first", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) { return nil, nil }})
	p.Add("second", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) {
		t.Fatal("empty successful catalogue triggered a fallback")
		return nil, nil
	}})
	models, err := p.ListModels(context.Background())
	if err != nil || len(models) != 0 {
		t.Fatalf("empty catalogue result changed: models=%v err=%v", models, err)
	}
}

func TestPoolListModelsRechecksAccountBeforeFallback(t *testing.T) {
	p := NewPool("test", nil)
	p.Add("first", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) {
		if err := p.SetAccountEnabled("second", false); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("configuration unavailable")
	}})
	p.Add("second", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) {
		t.Fatal("fallback used an account disabled while first request was pending")
		return nil, nil
	}})
	if _, err := p.ListModels(context.Background()); err == nil {
		t.Fatal("failed catalogue query returned no error")
	}
}

func TestPoolListModelsReturnsLastFailure(t *testing.T) {
	p := NewPool("test", nil)
	lastErr := errors.New("last catalogue failure")
	p.Add("first", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) {
		return nil, errors.New("first catalogue failure")
	}})
	p.Add("second", &catalogPoolFixture{list: func(context.Context) ([]ModelInfo, error) { return nil, lastErr }})
	if _, err := p.ListModels(context.Background()); !errors.Is(err, lastErr) {
		t.Fatalf("last upstream failure lost: %v", err)
	}
}
