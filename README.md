# go-pprof

[Русская версия](READMEru.md)

Production-safe [`net/http/pprof`](https://pkg.go.dev/net/http/pprof) for Go services: a separate pprof server on localhost with graceful shutdown, limits that protect a busy service from expensive profiling requests, and access control with basic auth or an IP allowlist.

No dependencies outside the standard library. Requires Go 1.21+.

## Why not just import net/http/pprof

The standard handlers do exactly what the client asks. On a loaded service some requests are expensive:

| Request | What it does |
| --- | --- |
| `/profile?seconds=3600` | CPU profiling overhead for an hour; a second profile fails with 500 |
| `/trace?seconds=600` | Execution trace overhead and hundreds of megabytes of output |
| `/heap?seconds=3600` | Holds a connection for an hour (delta profile) |
| `/heap?gc=1` in a loop | A full garbage collection on every call |
| `/goroutine?debug=2` | Pauses the program to dump every goroutine; huge with 100k+ goroutines |
| Slow or stuck client | Holds the connection forever (no write timeout is possible for streaming endpoints) |

This package puts a guard in front of the handlers with safe defaults, so a careless script or a curious colleague cannot slow down production.

## Installation

```
go get github.com/jwm1rr0rb10/go-pprof
```

## Usage

### Standalone server (recommended)

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

	// 127.0.0.1:6060 with the default limits.
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

`Run` opens the socket synchronously, so errors like "address already in use" are returned immediately. It returns `nil` after a normal shutdown.

To listen on a random free port or a Unix socket, pass your own listener to `Serve`:

```go
ln, err := net.Listen("tcp", "127.0.0.1:0")
if err != nil {
	log.Fatal(err)
}
log.Printf("pprof on http://%s/debug/pprof/", ln.Addr())
go srv.Serve(ctx, ln)
```

### Production configuration

```go
srv := pprof.NewServer(pprof.Config{
	Host:        "0.0.0.0", // reachable from the internal network only
	Middlewares: []pprof.Middleware{allow, pprof.BasicAuth("admin", os.Getenv("PPROF_PASSWORD"))},
	Limits: pprof.Limits{
		MaxProfileDuration: 30 * time.Second,
		MaxTraceDuration:   5 * time.Second,
		MaxConcurrent:      2,
		DisabledEndpoints:  []string{"cmdline"}, // flags may contain secrets
		OnRequest: func(i pprof.RequestInfo) {
			slog.Info("pprof request",
				"endpoint", i.Endpoint, "remote", i.RemoteAddr, "status", i.Status,
				"seconds", i.Seconds, "duration", i.Duration, "rejected", i.Rejected)
		},
	},
	// Enable /block and /mutex with low overhead.
	BlockProfileRate:     pprof.RecommendedBlockProfileRate,
	MutexProfileFraction: pprof.RecommendedMutexProfileFraction,
})
```

Middlewares run before the limits, so unauthenticated requests never take a concurrency slot.

### Mounting on an existing mux

```go
mux := http.NewServeMux()
pprof.Register(mux, pprof.Guard(pprof.Limits{}))
mux.HandleFunc("/api/health", healthHandler)

// Only on a trusted network: pprof is now served wherever mux is.
http.ListenAndServe("127.0.0.1:8080", mux)
```

`Register` adds no limits by itself; pass `Guard` as the last middleware, after any authentication. `pprof.Handler(...)` returns a ready `http.Handler` if you prefer to mount it yourself.

## Limits

The zero value of `pprof.Limits` is a safe production configuration.

| Field | Default | Effect |
| --- | --- | --- |
| `MaxProfileDuration` | 60s | Caps `seconds` of `/profile` and of delta profiles (`/heap?seconds=N`, ...). Longer requests are clamped, not rejected. |
| `MaxTraceDuration` | 10s | Caps `seconds` of `/trace`. |
| `MaxConcurrent` | 4 | Pprof requests served at once; extra ones get `429` with `Retry-After`. Only one `/profile` and one `/trace` run at a time regardless. |
| `WriteTimeout` | 30s | Time allowed to write the response after collection. The write deadline is collection time + `WriteTimeout`, so stuck clients free their slot. |
| `AllowForcedGC` | false | `/heap?gc=1` is rejected with `403` unless true. |
| `AllowFullGoroutineDump` | false | `/goroutine?debug=2` is rejected with `403` unless true. `/goroutine?debug=1` (aggregated stacks) always works. |
| `DisabledEndpoints` | none | Endpoints that return `404`: `"index"`, `"cmdline"`, `"profile"`, `"symbol"`, `"trace"`, or a profile name such as `"heap"`. |
| `OnRequest` | nil | Called after every pprof request, including rejected ones, with a `RequestInfo`. For audit logs and metrics; keep it fast. |

A negative duration or `MaxConcurrent` means no limit. Only `/symbol` accepts `POST`; other endpoints answer `405`, because the standard handlers also read parameters from a form body, which could bypass the duration caps.

## Access control

```go
allow, err := pprof.AllowNetworks("10.0.0.0/8", "127.0.0.1")
if err != nil {
	log.Fatal(err)
}
```

`AllowNetworks` accepts CIDR prefixes and single IPs, IPv4 and IPv6. It checks only `r.RemoteAddr` and ignores `X-Forwarded-For`, because clients can forge that header; behind a reverse proxy it sees the proxy's address.

`BasicAuth` compares credentials in constant time and panics on an empty username or password. Basic auth sends credentials in clear text, so use it over TLS or on a private network.

Any `func(http.Handler) http.Handler` works as a middleware.

## Server configuration

```go
pprof.Config{
	Host:                 "127.0.0.1",      // default
	Port:                 6060,             // default
	ReadHeaderTimeout:    10 * time.Second, // default
	IdleTimeout:          60 * time.Second, // default
	ShutdownTimeout:      15 * time.Second, // default
	Middlewares:          nil,
	Limits:               pprof.Limits{},   // safe defaults
	BlockProfileRate:     0,                // 0 = leave runtime setting unchanged
	MutexProfileFraction: 0,                // 0 = leave runtime setting unchanged
}
```

Zero values are replaced with defaults. There is no server-wide `WriteTimeout`, because `/profile` and `/trace` stream for as long as requested; `Guard` sets a per-request write deadline instead. `BlockProfileRate` and `MutexProfileFraction` are process-wide runtime settings applied when the server starts. The recommended values (`10_000` ns and `100`) keep the overhead low; `1` records every event and is too expensive for production.

A `Server` is single-use: after shutdown it cannot be started again. Its methods are safe to call from different goroutines.

## Security notes

Never expose pprof to the internet. Profiles reveal memory contents, command-line arguments, goroutine stacks and internal structure.

This package imports `net/http/pprof`, whose `init` function registers all handlers on `http.DefaultServeMux`. No wrapper can prevent that. If your application serves `http.DefaultServeMux` (for example `http.ListenAndServe(addr, nil)` or `http.Handle(...)`), pprof will be reachable there too, without limits. Always use your own `http.ServeMux` for public servers.

## Continuous profiling

For large production systems, HTTP pprof works best alongside a continuous profiler (Pyroscope, Grafana Profiles, Parca, Datadog). A continuous profiler samples constantly at low overhead and keeps history, so you can see what happened during last night's incident. Guarded HTTP pprof remains the tool for on-demand snapshots.

## Endpoints

| Endpoint | Description |
| --- | --- |
| `/debug/pprof/` | Index page |
| `/debug/pprof/profile` | CPU profile (`?seconds=N`, default 30, capped by `MaxProfileDuration`) |
| `/debug/pprof/heap` | Heap memory sampling |
| `/debug/pprof/allocs` | Past memory allocations |
| `/debug/pprof/goroutine` | Stacks of all goroutines |
| `/debug/pprof/block` | Blocking on synchronization primitives* |
| `/debug/pprof/mutex` | Mutex contention* |
| `/debug/pprof/threadcreate` | Stacks that created OS threads |
| `/debug/pprof/trace` | Execution trace (`?seconds=N`, default 1, capped by `MaxTraceDuration`) |
| `/debug/pprof/cmdline` | Program command line |
| `/debug/pprof/symbol` | Symbol lookup for program counters |

\* Empty unless enabled with `BlockProfileRate` / `MutexProfileFraction` (or `runtime.SetBlockProfileRate` / `runtime.SetMutexProfileFraction`).

Example:

```
go tool pprof -http=:8081 http://127.0.0.1:6060/debug/pprof/profile?seconds=10
```

## Changelog

### v1.1.0

Fixes:

- Data race between `Run` and `Shutdown`/`Close`; `Shutdown` before `Run` no longer lets the server start afterwards.
- `Run` now reports "address already in use" and other listen errors immediately.
- `Run` returns `nil` after a normal shutdown instead of `context.Canceled`.
- IPv6 hosts such as `::1` produce a valid address.
- Tests no longer take 30 seconds or depend on port 6060.

Added:

- `Limits` and `Guard`: duration caps, concurrency limits, one CPU profile and one trace at a time, blocking of `gc=1` and `debug=2`, disabled endpoints, per-request write deadlines, `OnRequest` hook.
- `BasicAuth`, `AllowNetworks`, `Middleware`, `Handler`, `Server.Serve`, `Server.Addr`.
- `Config.IdleTimeout`, `Config.ShutdownTimeout`, `Config.BlockProfileRate`, `Config.MutexProfileFraction`.
- CI with race tests on Go 1.21 and stable, and staticcheck.

Behavior changes:

- `Server` applies the default `Limits`: long profiles are clamped, `/heap?gc=1` and `/goroutine?debug=2` return `403`, and non-`GET` requests except to `/symbol` return `405`. Set the corresponding `Limits` fields to restore the old behavior. `Register` without `Guard` is unchanged.
- Minimum Go version lowered from 1.25 to 1.21.

## Development

```
make race   # tests with the race detector
make cover  # coverage report
make lint   # staticcheck
```

## License

[MIT](LICENSE) © Raman Zaitsau
