package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExecutionBudgetAndSuccessfulAttemptDoNotCancelLongStream(t *testing.T) {
	e := NewExecution(2)
	e.StartRouting(time.Second)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := WithAttemptAccount(WithExecution(parent, e), "test", "private-account")
	streamCtx, finish, err := BeginAttempt(ctx, "test", 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := finish(200, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	if streamCtx.Err() != nil {
		t.Fatal("header timer canceled an accepted stream")
	}
	_, finish, err = BeginAttempt(ctx, "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := finish(502, errors.New("temporary")); err == nil {
		t.Fatal("attempt error lost")
	}
	_, _, err = BeginAttempt(ctx, "test", time.Second)
	if f := Wrap(err); f.Code != "retry_budget_exhausted" || f.HTTPStatus() != 503 {
		t.Fatalf("budget=%v", err)
	}
	trace := e.Snapshot()
	if trace.AttemptCount != 2 || len(trace.RequestID) != 32 || trace.Attempts[0].AccountID == "private-account" {
		t.Fatal("incorrect or unsafe trace")
	}
	trace.Attempts[0].Platform = "changed"
	if e.Snapshot().Attempts[0].Platform != "test" {
		t.Fatal("snapshot aliases mutable state")
	}
}

func TestExecutionHeaderTimeoutAndCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		child, finish, err := BeginAttempt(ctx, "test", 5*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if cancelParent {
			cancel()
		}
		<-child.Done()
		err = finish(0, child.Err())
		if f := Wrap(err); cancelParent && f.HTTPStatus() != 499 || !cancelParent && f.HTTPStatus() != 504 {
			t.Fatalf("cancel=%v err=%v", cancelParent, err)
		}
		cancel()
	}
}
