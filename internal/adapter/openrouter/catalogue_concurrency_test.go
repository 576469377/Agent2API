package openrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestCatalogueWaitHonorsCancellation(t *testing.T) {
	for _, operation := range []string{"list", "save"} {
		t.Run(operation, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					t.Error("canceled request reached credential verification")
					return
				}
				close(started)
				<-release
				io.WriteString(w, catalogue)
			})
			a.cfg.CredentialPath = filepath.Join(t.TempDir(), "key.json")
			first := make(chan error, 1)
			go func() { _, err := a.ListModels(context.Background()); first <- err }()
			<-started
			ctx, cancel := context.WithCancel(context.Background())
			waiting := make(chan error, 1)
			go func() {
				if operation == "save" {
					waiting <- a.SaveAPIKey(ctx, "replacement-key")
					return
				}
				_, err := a.ListModels(ctx)
				waiting <- err
			}()
			cancel()
			select {
			case err := <-waiting:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled waiter returned %v", err)
				}
			case <-time.After(time.Second):
				t.Error("canceled waiter stayed blocked behind catalogue fetch")
			}
			close(release)
			if err := <-first; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCatalogueInvalidationDuringFetchRejectsNewlyPaidModel(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var fetches, generations atomic.Int32
	a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			generations.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if fetches.Add(1) == 1 {
			close(started)
			<-release
			io.WriteString(w, catalogue)
			return
		}
		io.WriteString(w, `{"data":[{"id":"vendor/free:free","pricing":{"prompt":"1","completion":"1"}}]}`)
	})
	done := make(chan error, 1)
	go func() {
		_, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free"})
		done <- err
	}()
	<-started
	a.InvalidateModels()
	close(release)
	if err := <-done; err == nil || llm.Wrap(err).Code != "free_model_required" {
		t.Errorf("invalidated free catalogue was used: %v", err)
	}
	if fetches.Load() != 2 || generations.Load() != 0 {
		t.Fatalf("fetches=%d generations=%d; expected fresh pricing and no generation", fetches.Load(), generations.Load())
	}
}

func TestCatalogueReturnedPricingCannotMutateCache(t *testing.T) {
	a := testAdapter(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, catalogue) })
	for range 2 {
		models, err := a.ListModels(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if models[0].Pricing.Status != "free" {
			t.Fatalf("a caller changed cached pricing: %+v", models[0].Pricing)
		}
		models[0].Pricing.Status = "caller-mutated"
	}
}

func TestConcurrentCatalogueReadsShareOneFetch(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var fetches atomic.Int32
	a := testAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
		if fetches.Add(1) == 1 {
			close(started)
			<-release
		}
		io.WriteString(w, catalogue)
	})
	const readers = 20
	done := make(chan error, readers)
	var entering sync.WaitGroup
	entering.Add(readers)
	for range readers {
		go func() {
			entering.Done()
			models, err := a.ListModels(context.Background())
			if err == nil && len(models) != 2 {
				t.Errorf("incomplete catalogue: %d models", len(models))
			}
			done <- err
		}()
	}
	entering.Wait()
	<-started
	close(release)
	for range readers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("concurrent readers made %d requests", fetches.Load())
	}
}
