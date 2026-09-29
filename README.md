# go-pprof

[Русская версия](READMEru.md)

A small wrapper around [`net/http/pprof`](https://pkg.go.dev/net/http/pprof) for Go applications: mount pprof on your own mux or run a separate pprof server on localhost with graceful shutdown, and optionally protect the endpoints with basic auth or an IP allowlist.

No dependencies outside the standard library. Requires Go 1.21+.

## Installation

```
go get github.com/jwm1rr0rb10/go-pprof
```

## Usage

### Standalone server on localhost (recommended)

Running pprof on a separate port bound to `127.0.0.1` keeps it away from your public API.

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jwm1rr0rb10/go-pprof"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Config{} listens on 127.0.0.1:6060.
	srv := pprof.NewServer(pprof.Config{})

	go func() {
		if err := srv.Run(ctx); err != nil {
			log.Printf("pprof: %v", err)
		}
	}()

	// ... your application ...

	<-ctx.Done()
}
```

`Run` opens the socket synchronously, so errors like "address already in use" are returned immediately. It returns `nil` after a normal shutdown, so there is no need to filter out `context.Canceled`.

To listen on a random free port or a Unix socket, create the listener yourself and pass it to `Serve`:

```go
ln, err := net.Listen("tcp", "127.0.0.1:0")
if err != nil {
	log.Fatal(err)
}
log.Printf("pprof on http://%s/debug/pprof/", ln.Addr())
go srv.Serve(ctx, ln)
```

### Mounting on an existing mux

```go
mux := http.NewServeMux()
pprof.Register(mux)
mux.HandleFunc("/api/health", healthHandler)

// Only on a trusted network: pprof is now served wherever mux is.
http.ListenAndServe("127.0.0.1:8080", mux)
```

If this mux is reachable from outside, protect the endpoints (see below). `pprof.Handler(...)` returns a ready `http.Handler` if you prefer to mount it yourself.

### Protecting the endpoints

Both `Register` and `Config.Middlewares` accept middleware. The first middleware in the list is the outermost.

```go
allow, err := pprof.AllowNetworks("10.0.0.0/8", "127.0.0.1")
if err != nil {
	log.Fatal(err)
}

pprof.Register(mux, allow, pprof.BasicAuth("admin", os.Getenv("PPROF_PASSWORD")))

// or for the standalone server:
srv := pprof.NewServer(pprof.Config{
	Host:        "0.0.0.0",
	Middlewares: []pprof.Middleware{allow},
})
```

`BasicAuth` compares credentials in constant time and panics on an empty username or password. Basic auth sends credentials in clear text, so use it over TLS or on a private network.

`AllowNetworks` accepts CIDR prefixes and single IPs, IPv4 and IPv6. It checks only `r.RemoteAddr` and ignores `X-Forwarded-For`, because clients can forge that header. Behind a reverse proxy it will see the proxy's address.

Any `func(http.Handler) http.Handler` works as a middleware, so you can plug in your own auth.

## Configuration

```go
pprof.Config{
	Host:              "127.0.0.1",      // default
	Port:              6060,             // default
	ReadHeaderTimeout: 10 * time.Second, // default, protects against slow clients
	ShutdownTimeout:   15 * time.Second, // default, bounds graceful shutdown
	Middlewares:       nil,
}
```

Zero values are replaced with defaults. `NewConfig(host, port, readHeaderTimeout)` is kept for backward compatibility. No `WriteTimeout` is set on purpose, because `/profile` and `/trace` stream data for as long as the client asks (30 seconds by default for `/profile`).

A `Server` is single-use: after shutdown it cannot be started again. Its methods are safe to call from different goroutines.

## Security notes

Never expose pprof to the internet. Profiles reveal memory contents, command-line arguments, goroutine stacks and internal structure, and `/profile` and `/trace` can be used to load the CPU.

This package imports `net/http/pprof`, whose `init` function registers all handlers on `http.DefaultServeMux`. No wrapper can prevent that. If your application serves `http.DefaultServeMux` (for example `http.ListenAndServe(addr, nil)` or `http.Handle(...)`), pprof will be reachable there as well. Always use your own `http.ServeMux` for public servers.

## Endpoints

| Endpoint | Description |
| --- | --- |
| `/debug/pprof/` | Index page |
| `/debug/pprof/profile` | CPU profile (`?seconds=N`, default 30) |
| `/debug/pprof/heap` | Heap memory sampling |
| `/debug/pprof/allocs` | Past memory allocations |
| `/debug/pprof/goroutine` | Stacks of all goroutines |
| `/debug/pprof/block` | Blocking on synchronization primitives* |
| `/debug/pprof/mutex` | Mutex contention* |
| `/debug/pprof/threadcreate` | Stacks that created OS threads |
| `/debug/pprof/trace` | Execution trace (`?seconds=N`, default 1) |
| `/debug/pprof/cmdline` | Program command line |
| `/debug/pprof/symbol` | Symbol lookup for program counters |

\* Block and mutex profiles are empty unless enabled with `runtime.SetBlockProfileRate` and `runtime.SetMutexProfileFraction`.

Example:

```
go tool pprof -http=:8081 http://127.0.0.1:6060/debug/pprof/profile?seconds=10
```

## Development

```
make race   # tests with the race detector
make cover  # coverage report
make lint   # staticcheck
```

## License

[MIT](LICENSE) © Raman Zaitsau
