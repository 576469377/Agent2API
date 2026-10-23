package workbuddy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

type refreshRoundTripFunc func(*http.Request) (*http.Response, error)

func (f refreshRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func blockedRefresh(t *testing.T, auth *Auth) (<-chan struct{}, func(), *atomic.Int32) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var once, unlock sync.Once
	calls := &atomic.Int32{}
	auth.cli.http.Transport = refreshRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release // Deliberately allow a completed response after caller cancellation.
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"accessToken":"refreshed-token","refreshToken":"rotated-refresh","expiresIn":3600}}`)), Request: r}, nil
	})
	return started, func() { unlock.Do(func() { close(release) }) }, calls
}

func TestCredentialReadsDoNotWaitForRefreshNetwork(t *testing.T) {
	a, _ := reloginFixture(t)
	started, release, _ := blockedRefresh(t, a.auth)
	done := make(chan error, 1)
	go func() { done <- a.auth.Refresh(context.Background()) }()
	defer func() { release(); <-done }()
	<-started
	read := make(chan string, 1)
	go func() { read <- a.auth.BuildHeaders(headerModeFull).Get("Authorization") }()
	select {
	case header := <-read:
		if header != "Bearer old-token" {
			t.Fatal("pending refresh changed the current token")
		}
	case <-time.After(time.Second):
		t.Fatal("credential reads blocked behind refresh network I/O")
	}
}

func TestReloginSupersedesAnInFlightRefresh(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) { testReloginSupersedesRefresh(t, failure) })
	}
}

func testReloginSupersedesRefresh(t *testing.T, failure bool) {
	a, path := reloginFixture(t)
	started, release, _ := blockedRefresh(t, a.auth)
	if failure {
		transport := a.auth.cli.http.Transport
		a.auth.cli.http.Transport = refreshRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			response, err := transport.RoundTrip(r)
			response.StatusCode = http.StatusUnauthorized
			return response, err
		})
	}
	done := make(chan error, 1)
	go func() { done <- a.auth.Refresh(context.Background()) }()
	defer release()
	<-started
	replaced := make(chan error, 1)
	go func() {
		replaced <- a.ReplaceCredential(&Credential{UID: "fixture-user", Domain: "www.workbuddy.ai", TokenType: "Bearer", AccessToken: "new-login", RefreshToken: "<new-refresh>"})
	}()
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		release()
		<-done
		<-replaced
		t.Fatal("relogin was blocked behind the old session's refresh")
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("superseded refresh returned an obsolete error: %v", err)
	}
	loaded, err := loadCredentialFile(path)
	if err != nil || loaded.AccessToken != "new-login" || a.auth.Credential().AccessToken != "new-login" {
		t.Fatalf("old refresh overwrote a newer login: %v", err)
	}
}

func TestRefreshPersistenceFailureRetainsRotatedCredentialInMemory(t *testing.T) {
	a, path := reloginFixture(t)
	_, release, _ := blockedRefresh(t, a.auth)
	release()
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	err := a.auth.Refresh(context.Background())
	if err == nil || llm.Wrap(err).Code != "credential_write_failed" {
		t.Fatalf("refresh save error = %v", err)
	}
	if a.auth.Credential().RefreshToken != "rotated-refresh" {
		t.Fatal("save failure restored the now-obsolete refresh token")
	}
	saved, err := loadCredentialFile(path)
	if err != nil || saved.AccessToken != "old-token" {
		t.Fatalf("failed save replaced the existing credential file: %v", err)
	}
}

func TestConcurrentForcedRefreshesShareOneRotation(t *testing.T) {
	a, _ := reloginFixture(t)
	started, release, calls := blockedRefresh(t, a.auth)
	done := make(chan error, 2)
	go func() { done <- a.auth.Refresh(context.Background()) }()
	defer release()
	<-started
	second := make(chan struct{})
	go func() { close(second); done <- a.auth.Refresh(context.Background()) }()
	<-second
	waitRefreshWaiters(t, a.auth, 2)
	release()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("overlapping refreshes rotated the credential %d times", got)
	}
}

func waitRefreshWaiters(t *testing.T, auth *Auth, count int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		auth.mu.Lock()
		ready := auth.refreshing != nil && auth.refreshing.waiters == count
		auth.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("refresh did not acquire %d waiters", count)
		}
	}
}

func TestRefreshWaiterCancellationDoesNotCancelOtherCallers(t *testing.T) {
	a, _ := reloginFixture(t)
	started, release, calls := blockedRefresh(t, a.auth)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- a.auth.Refresh(ctx) }()
	<-started
	go func() { second <- a.auth.Refresh(context.Background()) }()
	waitRefreshWaiters(t, a.auth, 2)
	a.auth.mu.Lock()
	shared := a.auth.refreshing.ctx
	a.auth.mu.Unlock()
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled refresh = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled refresh caller kept waiting")
	}
	if shared.Err() != nil {
		t.Fatal("one cancelled caller aborted the shared refresh")
	}
	release()
	if err := <-second; err != nil || calls.Load() != 1 {
		t.Fatalf("remaining caller lost shared refresh: %v, requests=%d", err, calls.Load())
	}
}

func TestLastRefreshWaiterCancellationStopsNetwork(t *testing.T) {
	a, _ := reloginFixture(t)
	started, stopped := make(chan struct{}), make(chan struct{})
	a.auth.cli.http.Transport = refreshRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(stopped)
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.auth.Refresh(ctx) }()
	<-started
	a.auth.mu.Lock()
	finished := a.auth.refreshing.done
	a.auth.mu.Unlock()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh = %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("refresh kept using the network after its last caller left")
	}
	<-finished
	if a.auth.Credential().AccessToken != "old-token" {
		t.Fatal("cancelled refresh changed the credential")
	}
}

func TestCancelledRotationFinishesBeforeAnotherRefreshStarts(t *testing.T) {
	a, path := reloginFixture(t)
	started, release, calls := blockedRefresh(t, a.auth)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.auth.Refresh(ctx) }()
	<-started
	a.auth.mu.Lock()
	finished := a.auth.refreshing.done
	a.auth.mu.Unlock()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	joining, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if err := a.auth.Refresh(joining); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("new caller did not wait cancellably for the unfinished rotation: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("a cancelled but unfinished exchange overlapped %d refreshes", calls.Load())
	}
	release()
	<-finished
	saved, err := loadCredentialFile(path)
	if err != nil || saved.RefreshToken != "rotated-refresh" || a.auth.Credential().RefreshToken != "rotated-refresh" {
		t.Fatalf("completed server rotation was discarded after cancellation: %v", err)
	}
}

func TestProactiveRefreshAllowsValidCredentialButExpiredWaiterCanCancel(t *testing.T) {
	for _, remaining := range []time.Duration{30 * time.Second, -time.Second} {
		t.Run(remaining.String(), func(t *testing.T) {
			a, _ := reloginFixture(t)
			a.auth.mu.Lock()
			a.auth.cred.ExpiresAt = time.Now().Add(remaining).UnixMilli()
			a.auth.mu.Unlock()
			started, release, _ := blockedRefresh(t, a.auth)
			done := make(chan error, 1)
			go func() { done <- a.auth.EnsureValid(context.Background()) }()
			defer func() { release(); <-done }()
			<-started
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			err := a.auth.EnsureValid(ctx)
			if remaining > 0 && err != nil {
				t.Fatalf("unexpired credential blocked on refresh: %v", err)
			}
			if remaining < 0 && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expired credential bypassed refresh: %v", err)
			}
		})
	}
}
