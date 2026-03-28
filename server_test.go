package pprof

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewServer(t *testing.T) {
	cfg := NewConfig("localhost", 8080, 5*time.Second)
	server := NewServer(cfg)

	if server.address != "localhost:8080" {
		t.Errorf("expected address 'localhost:8080', got '%s'", server.address)
	}
	if server.readHeaderTimeout != 5*time.Second {
		t.Errorf("expected ReadHeaderTimeout 5s, got %v", server.readHeaderTimeout)
	}
}

func TestRegister(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)

	endpoints := []string{
		pprofURL, cmdlineURL, profileURL, symbolURL, traceURL,
		goroutineURL, heapURL, allocsURL, threadcreateURL, blockURL, mutexURL,
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, endpoint, nil)
			rr := httptest.NewRecorder()

			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("expected status 200 for %s, got %d", endpoint, rr.Code)
			}
		})
	}
}

func TestRunAndShutdown(t *testing.T) {
	cfg := NewConfig("127.0.0.1", 0, 0) // triggers defaults
	server := NewServer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)

	go func() {
		errCh <- server.Run(ctx)
	}()

	time.Sleep(100 * time.Millisecond) // give server time to start
	cancel()                           // trigger graceful shutdown

	if err := <-errCh; err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
