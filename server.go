package pprof

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"
)

const (
	pprofURL        = "/debug/pprof/"
	cmdlineURL      = "/debug/pprof/cmdline"
	profileURL      = "/debug/pprof/profile"
	symbolURL       = "/debug/pprof/symbol"
	traceURL        = "/debug/pprof/trace"
	goroutineURL    = "/debug/pprof/goroutine"
	heapURL         = "/debug/pprof/heap"
	allocsURL       = "/debug/pprof/allocs"
	threadcreateURL = "/debug/pprof/threadcreate"
	blockURL        = "/debug/pprof/block"
	mutexURL        = "/debug/pprof/mutex"
)

// Register adds all standard pprof handlers to the given mux.
// Use this when you want to mount pprof on your existing HTTP server.
func Register(mux *http.ServeMux) {
	mux.HandleFunc(pprofURL, pprof.Index)
	mux.HandleFunc(cmdlineURL, pprof.Cmdline)
	mux.HandleFunc(profileURL, pprof.Profile)
	mux.HandleFunc(symbolURL, pprof.Symbol)
	mux.HandleFunc(traceURL, pprof.Trace)
	mux.Handle(goroutineURL, pprof.Handler("goroutine"))
	mux.Handle(heapURL, pprof.Handler("heap"))
	mux.Handle(allocsURL, pprof.Handler("allocs"))
	mux.Handle(threadcreateURL, pprof.Handler("threadcreate"))
	mux.Handle(blockURL, pprof.Handler("block"))
	mux.Handle(mutexURL, pprof.Handler("mutex"))
}

// Server is a standalone pprof HTTP server (recommended for security – run on a separate port).
type Server struct {
	address           string
	readHeaderTimeout time.Duration
	httpServer        *http.Server
}

// NewServer creates a new pprof server.
// Defaults are applied: Host="", Port=0, ReadHeaderTimeout=0 → localhost:6060 with 10s timeout.
func NewServer(cfg Config) *Server {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1" // security best practice – pprof leaks sensitive data
	}
	if cfg.Port == 0 {
		cfg.Port = 6060 // conventional pprof port
	}
	if cfg.ReadHeaderTimeout == 0 {
		cfg.ReadHeaderTimeout = 10 * time.Second
	}

	return &Server{
		address:           fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		readHeaderTimeout: cfg.ReadHeaderTimeout,
	}
}

// Run starts the pprof server and blocks until the context is canceled or an error occurs.
// It performs a graceful shutdown when the context is done.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	Register(mux)

	s.httpServer = &http.Server{
		Addr:              s.address,
		Handler:           mux,
		ReadHeaderTimeout: s.readHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("pprof server shutdown: %w", err)
		}
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// Shutdown gracefully shuts down the server (preferred over Close).
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// Close immediately closes all connections (use only as last resort).
func (s *Server) Close() error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Close()
}
