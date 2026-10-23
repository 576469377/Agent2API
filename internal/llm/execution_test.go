package llm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestUploadProgressSnapshotsAreIndependentAndIgnoreOlderAttemptCallbacks(t *testing.T) {
	e := NewExecution(6)
	ctx := WithExecution(context.Background(), e)
	first, finish, err := BeginAttempt(ctx, "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	SetRequestBytes(first, 4096)
	ObserveUploadConnection(first, "HTTP/1.1", false)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			ObserveUpload(first, 32)
		}
	}()
	for i := 0; i < 100; i++ {
		_ = e.Snapshot()
	}
	wg.Wait()
	before := e.Snapshot()
	if before.Attempts[0].UploadedBytes != 3200 || before.Attempts[0].UploadStartedAtMs == 0 {
		t.Fatal("upload progress not recorded")
	}
	ObserveUpload(first, 32)
	if before.Attempts[0].UploadedBytes != 3200 {
		t.Fatal("snapshot aliases mutable progress")
	}
	_ = finish(502, errors.New("fixture failure"))
	second, finishSecond, err := BeginAttempt(ctx, "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	SetRequestBytes(second, 10)
	ObserveUpload(first, 1000)
	CompleteUpload(first)
	ObserveUploadConnection(first, "HTTP/2.0", true)
	CompleteUpload(second)
	after := e.Snapshot()
	if after.Attempts[0].UploadedBytes != 3232 || after.Attempts[0].UploadCompletedAtMs != 0 || after.Attempts[1].UploadedBytes != 0 || after.Attempts[1].UploadCompletedAtMs == 0 {
		t.Fatalf("late callback contaminated progress: %+v", after)
	}
	_ = finishSecond(200, nil)
}

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

func TestAttemptRecordsFinalTimeoutStatusAndCause(t *testing.T) {
	for _, routingTimeout := range []bool{false, true} {
		e := NewExecution(6)
		parent, cancel := context.WithCancelCause(context.Background())
		ctx := WithExecution(parent, e)
		child, finish, err := BeginAttempt(ctx, "test", 20*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		wantStatus, wantCode := 504, "upstream_header_timeout"
		if routingTimeout {
			wantStatus, wantCode = 503, "routing_timeout"
			cancel(&Failure{Code: wantCode, Message: "routing deadline", Local: true})
		}
		<-child.Done()
		err = finish(0, child.Err())
		trace := e.Snapshot().Attempts[0]
		if f := Wrap(err); f.Code != wantCode || f.HTTPStatus() != wantStatus || trace.Status != wantStatus || trace.ErrorCode != wantCode {
			t.Fatalf("timeout shown as client cancellation: error=%v trace=%+v", err, trace)
		}
		cancel(nil)
	}
}
