package pprof

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewServerDefaults(t *testing.T) {
	s := NewServer(Config{})
	if got, want := s.Addr(), "127.0.0.1:6060"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
	if s.srv.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", s.srv.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	}
	if s.shutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("shutdownTimeout = %v, want %v", s.shutdownTimeout, DefaultShutdownTimeout)
	}
}

func TestNewServerCustom(t *testing.T) {
	s := NewServer(NewConfig("localhost", 8080, 5*time.Second))
	if got, want := s.Addr(), "localhost:8080"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
	if s.srv.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 5s", s.srv.ReadHeaderTimeout)
	}
}

func TestNewServerIPv6(t *testing.T) {
	s := NewServer(Config{Host: "::1", Port: 6061})
	if got, want := s.Addr(), "[::1]:6061"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

// Short durations keep /profile and /trace from blocking for 30s / 1s.
var endpoints = []string{
	"/debug/pprof/",
	"/debug/pprof/cmdline",
	"/debug/pprof/profile?seconds=1",
	"/debug/pprof/symbol",
	"/debug/pprof/trace?seconds=0.1",
	"/debug/pprof/goroutine?debug=1",
	"/debug/pprof/heap",
	"/debug/pprof/allocs",
	"/debug/pprof/threadcreate",
	"/debug/pprof/block",
	"/debug/pprof/mutex",
}

func TestRegister(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, ep, nil))
			if rr.Code != http.StatusOK {
				t.Errorf("GET %s: status %d, want 200", ep, rr.Code)
			}
		})
	}
}

func TestRegisterUnknownProfile(t *testing.T) {
	rr := httptest.NewRecorder()
	Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/debug/pprof/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rr.Code)
	}
}

func TestRegisterMiddlewareCoversAllEndpoints(t *testing.T) {
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
	}
	h := Handler(deny)
	for _, ep := range endpoints {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, ep, nil))
		if rr.Code != http.StatusTeapot {
			t.Errorf("GET %s: middleware not applied, status %d", ep, rr.Code)
		}
	}
}

func TestMiddlewareOrder(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), []Middleware{mw("a"), nil, mw("b")})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if got := strings.Join(order, ","); got != "a,b" {
		t.Errorf("order = %q, want %q", got, "a,b")
	}
}

func localListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready", url)
}

func TestServeAndGracefulShutdown(t *testing.T) {
	ln := localListener(t)
	s := NewServer(Config{})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx, ln) }()

	waitReady(t, "http://"+ln.Addr().String()+"/debug/pprof/")
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestShutdownFromAnotherGoroutine(t *testing.T) {
	ln := localListener(t)
	s := NewServer(Config{})

	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(context.Background(), ln) }()

	waitReady(t, "http://"+ln.Addr().String()+"/debug/pprof/")
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Errorf("Serve returned %v, want nil", err)
	}
}

func TestShutdownBeforeServe(t *testing.T) {
	s := NewServer(Config{})
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := s.Serve(context.Background(), localListener(t)); err != nil {
		t.Errorf("Serve after Shutdown returned %v, want nil", err)
	}
}

func TestRunReturnsListenErrorImmediately(t *testing.T) {
	busy := localListener(t)
	defer busy.Close()

	addr := busy.Addr().(*net.TCPAddr)
	s := NewServer(Config{Host: addr.IP.String(), Port: addr.Port})

	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(context.Background()) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run on a busy port returned nil")
		}
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Errorf("expected wrapped *net.OpError, got %T: %v", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run on a busy port did not fail immediately")
	}
}
