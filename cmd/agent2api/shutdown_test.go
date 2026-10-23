package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/obs"
)

func TestShutdownTimeoutCancelsAndPersistsTheFinalRequest(t *testing.T) {
	metrics := obs.New()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		id := metrics.BeginLive("test", now)
		close(started)
		<-r.Context().Done()
		close(cancelled)
		<-release // Simulate the request's final accounting during cancellation.
		metrics.Record(obs.Record{LiveID: id, Time: now, ErrorCode: "client_cancelled", InputTokens: 7, OutputTokens: 11})
	})}
	requests := newRequestDrain(server.Handler)
	server.Handler = requests
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close(); close(release) })
	go server.Serve(listener)
	go func() {
		if response, err := http.Get("http://" + listener.Addr().String()); err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	saved := make(chan int64, 1)
	go func() {
		done <- shutdownAndPersist(ctx, server, requests, func() { saved <- metrics.Snapshot().Total })
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown timeout did not cancel the active request")
	}
	select {
	case total := <-saved:
		t.Fatalf("persisted before the cancelled request finished accounting: %d", total)
	default:
	}
	release <- struct{}{}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown = %v, want original deadline error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not complete after request accounting")
	}
	if total := <-saved; total != 1 {
		t.Fatalf("final cancelled request missing: %d", total)
	}
	if snapshot := metrics.Snapshot(); snapshot.InputTokens != 7 || snapshot.OutputTokens != 11 {
		t.Fatalf("partial tokens lost on shutdown: %+v", snapshot)
	}
}

func TestShutdownPersistsTheRequestWhichFinishesDuringDrain(t *testing.T) {
	metrics := obs.New()
	started := make(chan struct{})
	release := make(chan struct{})
	draining := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		id := metrics.BeginLive("test", now)
		close(started)
		<-release
		metrics.Record(obs.Record{LiveID: id, Time: now, OK: true, InputTokens: 2, OutputTokens: 3})
		io.WriteString(w, "OK")
	})}
	requests := newRequestDrain(server.Handler)
	server.Handler = requests
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go server.Serve(listener)
	server.RegisterOnShutdown(func() { close(draining) })
	clientDone := make(chan error, 1)
	go func() {
		r, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			_, err = io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		clientDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	path := filepath.Join(t.TempDir(), "metrics.json")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	saved := make(chan int64, 1)
	go func() {
		shutdownDone <- shutdownAndPersist(ctx, server, requests, func() {
			saved <- metrics.Snapshot().Total
			if err := metrics.Save(path); err != nil {
				t.Error(err)
			}
		})
	}()
	select {
	case <-draining:
	case <-ctx.Done():
		t.Fatal("shutdown did not start")
	}
	select {
	case total := <-saved:
		t.Fatalf("saved before the active request finished: %d", total)
	default:
	}
	close(release)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	if total := <-saved; total != 1 {
		t.Fatalf("final completed request missing: %d", total)
	}
	restored := obs.New()
	if err := restored.Load(path); err != nil {
		t.Fatal(err)
	}
	if s := restored.Snapshot(); s.Total != 1 || s.InputTokens != 2 || s.OutputTokens != 3 {
		t.Fatalf("tail request lost on restart: %+v", s)
	}
}
