package main

import (
	"context"
	"testing"
	"time"
)

func TestPeriodicStopsWhileWaitingForNextPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	called := make(chan struct{}, 1)
	go func() {
		runPeriodic(ctx, time.Hour, func() { called <- struct{}{} })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic worker did not stop on server cancellation")
	}
	select {
	case <-called:
		t.Fatal("periodic worker ran before its first scheduled interval")
	default:
	}
}

func TestPeriodicStopsAfterCurrentPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	passes := 0
	go func() {
		runPeriodic(ctx, time.Millisecond, func() {
			passes++
			cancel()
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic worker did not finish its first scheduled pass")
	}
	if passes != 1 {
		t.Fatalf("periodic worker ran %d passes, want exactly one", passes)
	}
}
