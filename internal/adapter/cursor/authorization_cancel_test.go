package cursor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestCredentialReadCancellationPreservesReadyState(t *testing.T) {
	a, _, calls := testAdapter(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected generation") })
	if _, err := a.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	a.load = func(ctx context.Context, _ string) (credentials, error) {
		close(started)
		<-ctx.Done()
		// The desktop reader may only expose a sanitized credential message.
		return credentials{}, fmt.Errorf("Cursor 登录态不可读")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := a.ListModels(ctx); done <- err }()
	<-started
	cancel()
	err := <-done
	f := llm.Wrap(err)
	if !errors.Is(err, context.Canceled) || f.HTTPStatus() != 499 || f.Unauthorized {
		t.Fatalf("canceled credential read was reported as login failure: %+v", f)
	}
	if a.Describe().Status != "active" {
		t.Fatal("canceling a catalogue read removed the confirmed account state")
	}
	if calls.Load() != 0 {
		t.Fatal("metadata cancellation generated usage")
	}
}

func TestCanceledCatalogueRequestSkipsCredentialReader(t *testing.T) {
	a, _, _ := testAdapter(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected generation") })
	a.load = func(context.Context, string) (credentials, error) {
		t.Error("already canceled request read local credentials")
		return credentials{}, fmt.Errorf("missing credentials")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.ListModels(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

type cancelMetadataTransport func(*http.Request) (*http.Response, error)

func (f cancelMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMetadataCancellationPreservesReadyState(t *testing.T) {
	for _, path := range []string{"/auth/full_stripe_profile", "/aiserver.v1.DashboardService/GetHardLimit", "/aiserver.v1.ServerConfigService/GetServerConfig", "/agent.v1.AgentService/GetUsableModels"} {
		t.Run(path, func(t *testing.T) {
			a, _, calls := testAdapter(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected generation") })
			if _, err := a.ListModels(context.Background()); err != nil {
				t.Fatal(err)
			}
			base := a.client.Transport
			started := make(chan struct{})
			a.client.Transport = cancelMetadataTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != path {
					return base.RoundTrip(r)
				}
				close(started)
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { _, err := a.ListModels(ctx); done <- err }()
			<-started
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) || llm.Wrap(err).HTTPStatus() != 499 {
				t.Fatalf("metadata cancellation was lost: %v", err)
			}
			if a.Describe().Status != "active" || calls.Load() != 0 {
				t.Fatal("cancellation changed account state or generated usage")
			}
		})
	}
}
