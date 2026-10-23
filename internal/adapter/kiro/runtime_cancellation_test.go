package kiro

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestRPCCallCancellationInterruptsBlockedWrite(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	connection, stop := context.WithCancel(context.Background())
	defer stop()
	c := &rpcClient{in: writer, ctx: connection, cancel: stop, writeGate: make(chan struct{}, 1), pending: map[string]chan rpcReply{}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- c.call(ctx, "session/prompt", map[string]string{"prompt": "test"}, nil) }()
	// Receiving only the first byte leaves the JSON-RPC frame blocked in Write.
	if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RPC call remained blocked writing after its context was cancelled")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) != 0 {
		t.Fatalf("cancelled RPC left %d pending calls", len(c.pending))
	}
	if connection.Err() == nil {
		t.Fatal("partially written RPC connection remained reusable")
	}
}

func TestRPCCancelQueuedWritePreservesActiveFrame(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	connection, stop := context.WithCancel(context.Background())
	defer stop()
	c := &rpcClient{in: writer, ctx: connection, cancel: stop, writeGate: make(chan struct{}, 1)}
	active := make(chan error, 1)
	go func() { active <- c.write(map[string]string{"method": "active"}) }()
	if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { queued <- c.writeContext(ctx, map[string]string{"method": "queued"}) }()
	cancel()
	select {
	case err := <-queued:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued write cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled writer remained queued")
	}
	if connection.Err() != nil {
		t.Fatal("queued writer cancellation closed the active connection")
	}
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	select {
	case err := <-active:
		if err != nil {
			t.Fatalf("active frame failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active frame did not finish")
	}
	if err := c.write(map[string]string{"method": "next"}); err != nil {
		t.Fatalf("connection could not write the next frame: %v", err)
	}
}

func TestRPCCompletedWriteDetachesCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	connection, stop := context.WithCancel(context.Background())
	defer stop()
	c := &rpcClient{in: writer, ctx: connection, cancel: stop, writeGate: make(chan struct{}, 1)}
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.writeContext(ctx, map[string]string{"method": "finished"}); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := c.write(map[string]string{"method": "next"}); err != nil {
		t.Fatalf("a completed call's cancellation interrupted the next frame: %v", err)
	}
	if connection.Err() != nil {
		t.Fatal("a completed write left its cancellation hook attached")
	}
}
