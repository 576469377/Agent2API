package workbuddy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Stall an encrypted DATA write after connection setup and request headers.
// Unlike an unread HTTP/2 stream body, a blocked socket write holds the shared
// HTTP/2 writer lock and can prevent unrelated request headers from being sent.
type catalogWriteStallConn struct {
	net.Conn
	armed, stalled *atomic.Bool
	entered        chan struct{}
	release        <-chan struct{}
}

func (c *catalogWriteStallConn) Write(p []byte) (int, error) {
	if c.armed.Load() && len(p) > 8192 && c.stalled.CompareAndSwap(false, true) {
		close(c.entered)
		<-c.release
	}
	return c.Conn.Write(p)
}

func TestCatalogReadSurvivesStalledChatSocketWrite(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == configPath {
			_, _ = io.WriteString(w, `{"code":0,"data":{"models":[{"id":"catalog-fixture","name":"Fixture","maxOutputTokens":1024}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	c := newHTTPClient(server.URL, 0)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, client := range []*http.Client{c.http, c.streamHTTP} {
		transport := client.Transport.(*http.Transport)
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.RootCAs = roots
		transport.Proxy = nil
		t.Cleanup(transport.CloseIdleConnections)
	}

	var armed, stalled atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	c.streamHTTP.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &catalogWriteStallConn{Conn: conn, armed: &armed, stalled: &stalled, entered: entered, release: release}, nil
	}
	// Establish TLS and protocol negotiation before arming the write fault.
	warmupCtx, cancelWarmup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWarmup()
	warmupRequest, _ := http.NewRequestWithContext(warmupCtx, http.MethodGet, server.URL+"/warmup", nil)
	warmup, err := c.streamHTTP.Do(warmupRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, warmup.Body)
	warmup.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	armed.Store(true)

	uploadCtx, cancelUpload := context.WithTimeout(context.Background(), 10*time.Second)
	uploadConnection := make(chan net.Conn, 1)
	uploadCtx = httptrace.WithClientTrace(uploadCtx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		uploadConnection <- info.Conn
	}})
	uploadDone := make(chan struct{})
	t.Cleanup(func() {
		unblock()
		cancelUpload()
		select {
		case <-uploadDone:
		case <-time.After(5 * time.Second):
			t.Error("upload did not exit after its socket write was released")
		}
	})
	go func() {
		defer close(uploadDone)
		response, err := c.openStream(uploadCtx, "/upload", map[string]string{"fixture": strings.Repeat("x", 2<<20)}, http.Header{})
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-uploadCtx.Done():
		t.Fatal("chat upload did not reach the injected socket stall")
	}
	chatConn := <-uploadConnection

	var sharedConnection, headersSent atomic.Bool
	catalogCtx, cancelCatalog := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCatalog()
	catalogCtx = httptrace.WithClientTrace(catalogCtx, &httptrace.ClientTrace{
		GotConn:      func(info httptrace.GotConnInfo) { sharedConnection.Store(info.Conn == chatConn) },
		WroteHeaders: func() { headersSent.Store(true) },
	})
	a := &Adapter{cli: c, auth: newAuth(c, &Credential{Domain: "www.workbuddy.ai"}, "")}
	models, err := a.ReadModelCatalog(catalogCtx)
	if err != nil {
		t.Fatalf("catalogue read blocked by a stalled chat socket write (chat protocol=%s, shared connection=%v, headers sent=%v): %v", warmup.Proto, sharedConnection.Load(), headersSent.Load(), err)
	}
	if len(models) != 1 || models[0].ID != "catalog-fixture" || !headersSent.Load() || sharedConnection.Load() {
		t.Fatalf("catalogue did not complete independently: models=%v headers=%v shared=%v", models, headersSent.Load(), sharedConnection.Load())
	}
}
