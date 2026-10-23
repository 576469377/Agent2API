package workbuddy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A stalled connection belonging to one account must not carry another
// account's requests, even when both use the same HTTP/2 upstream origin.
func TestAccountClientsDoNotShareHTTP2Connections(t *testing.T) {
	type connectionKey struct{}
	var connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Context().Value(connectionKey{}).(int64)
		if id == 1 && r.URL.Path != "/warmup" {
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	server.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, connectionKey{}, connections.Add(1))
	}
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	first := newHTTPClient(server.URL, 0)
	second := newHTTPClient(server.URL, 0)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, client := range []*httpClient{first, second} {
		transport, ok := client.http.Transport.(*http.Transport)
		if !ok {
			t.Fatal("account client must own a configurable HTTP transport")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots}
		t.Cleanup(transport.CloseIdleConnections)
	}

	response, err := first.http.Get(server.URL + "/warmup")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("fixture must exercise HTTP/2, got %s", response.Proto)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/probe", nil)
	response, err = second.http.Do(request)
	if err != nil {
		t.Fatalf("second account reused the stalled first connection: %v", err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 || connections.Load() != 2 {
		t.Fatalf("accounts must use separate reusable HTTP/2 connections: protocol=%s connections=%d", response.Proto, connections.Load())
	}
	if first.http.Timeout != 0 || second.http.Timeout != 0 {
		t.Fatal("connection isolation must not add a client timeout that truncates long streams")
	}
}
