package pprof

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"
)

// Endpoint paths. Named profiles (heap, goroutine, allocs, block, mutex,
// threadcreate) are served by the index handler under PathPrefix.
const (
	PathPrefix  = "/debug/pprof/"
	cmdlinePath = PathPrefix + "cmdline"
	profilePath = PathPrefix + "profile"
	symbolPath  = PathPrefix + "symbol"
	tracePath   = PathPrefix + "trace"
)

// Register mounts all standard pprof handlers on mux under /debug/pprof/.
// Optional middlewares wrap every handler; the first one is the outermost.
//
// Note: this exposes pprof on whatever address mux is served on. Prefer a
// separate [Server] on localhost, or protect the endpoints with
// [BasicAuth] / [AllowNetworks].
func Register(mux *http.ServeMux, mws ...Middleware) {
	handle := func(path string, h http.HandlerFunc) {
		mux.Handle(path, chain(h, mws))
	}
	// pprof.Index also serves every named profile: /heap, /goroutine, ...
	handle(PathPrefix, pprof.Index)
	handle(cmdlinePath, pprof.Cmdline)
	handle(profilePath, pprof.Profile)
	handle(symbolPath, pprof.Symbol)
	handle(tracePath, pprof.Trace)
}

// Handler returns an http.Handler serving all pprof endpoints under
// /debug/pprof/, wrapped in the given middlewares.
func Handler(mws ...Middleware) http.Handler {
	mux := http.NewServeMux()
	Register(mux, mws...)
	return mux
}

// Server is a standalone pprof HTTP server.
// Running it on localhost on a separate port is the recommended setup.
//
// A Server is single-use: once it has been shut down it cannot be started
// again. Its methods are safe for concurrent use.
type Server struct {
	addr            string
	shutdownTimeout time.Duration
	srv             *http.Server
}

// NewServer creates a new pprof server. Zero-valued fields of cfg are
// replaced with defaults, so NewServer(Config{}) listens on 127.0.0.1:6060.
func NewServer(cfg Config) *Server {
	cfg = cfg.withDefaults()
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	return &Server{
		addr:            addr,
		shutdownTimeout: cfg.ShutdownTimeout,
		srv: &http.Server{
			Addr:              addr,
			Handler:           Handler(cfg.Middlewares...),
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		},
	}
}

// Addr returns the configured listen address ("host:port").
func (s *Server) Addr() string { return s.addr }

// Run listens on the configured address and serves until ctx is canceled,
// then shuts down gracefully (bounded by Config.ShutdownTimeout).
//
// The listening socket is opened synchronously, so an error such as
// "address already in use" is returned immediately.
// Run returns nil after a normal shutdown (ctx canceled, or [Server.Shutdown]
// / [Server.Close] called).
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("pprof: listen on %s: %w", s.addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve is like [Server.Run] but uses the provided listener, for example
// one bound to "127.0.0.1:0" or a Unix socket. Serve takes ownership of ln
// and closes it on return.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("pprof: serve: %w", err)

	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()

		shutdownErr := s.srv.Shutdown(shutdownCtx)
		<-errCh // Serve r/home/user/workspace/go-pprofeturns ErrServerClosed as soon as Shutdown starts.
		if shutdownErr != nil {
			return fmt.Errorf("pprof: shutdown: %w", shutdownErr)
		}
		return nil
	}
}

// Shutdown gracefully shuts down the server, waiting for active requests
// until ctx is done. Calling it before Run/Serve makes them return nil
// immediately without serving.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// Close immediately closes all listeners and connections.
// Prefer [Server.Shutdown]; use Close only as a last resort.
func (s *Server) Close() error {
	return s.srv.Close()
}
