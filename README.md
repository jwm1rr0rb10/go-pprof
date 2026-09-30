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
| `/goroutine` or `/heap` polled in a loop | Constant profiling cost; with 500k goroutines every call walks all their stacks |
| Slow or stuck client | Holds the connection forever (no write timeout is possible for streaming endpoints) |
| `POST /symbol` with a slow or huge body | The standard handler reads the body until EOF, holding the connection |

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
				"principal", i.Principal, "seconds", i.Seconds, "duration", i.Duration, "rejected", i.Rejected)
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
| `MaxConcurrent` | 4 | Pprof requests served at once; extra ones get `429` with `Retry-After`. Only one `/profile` and one `/trace` run at a time regardless; a second one gets `429` with `Retry-After` set to the time the running one still needs. |
| `MinInterval` | 1s | Minimum time between two requests to the same profile (`/heap`, `/goroutine`, `/profile`, ...). Each endpoint has its own clock; `/`, `/cmdline` and `/symbol` are not limited. Too frequent requests get `429` with the exact `Retry-After`. |
| `WriteTimeout` | 30s | Time allowed to write the response after collection. The write deadline is collection time + `WriteTimeout`, so stuck clients free their slot. |
| `ReadTimeout` | 10s | Time allowed to send the body of `POST /symbol`; slower clients get `408`. |
| `MaxSymbolBodyBytes` | 1 MiB | Maximum body of `POST /symbol`; larger bodies get `413`. |
| `AllowForcedGC` | false | `/heap?gc=1` is rejected with `403` unless true. |
| `AllowFullGoroutineDump` | false | `/goroutine?debug=2` is rejected with `403` unless true. `/goroutine?debug=1` (aggregated stacks) always works. |
| `DisabledEndpoints` | none | Endpoints that return `404`: `"index"`, `"cmdline"`, `"profile"`, `"symbol"`, `"trace"`, or a profile name such as `"heap"`. |
| `OnRequest` | nil | Called after every pprof request, including rejected ones, with a `RequestInfo`. For audit logs and metrics; keep it fast. |

A negative duration, `MaxConcurrent` or `MaxSymbolBodyBytes` means no limit. Only `/symbol` accepts `POST`; other endpoints answer `405`, because the standard handlers also read parameters from a form body, which could bypass the duration caps.

## Access control

```go
allow, err := pprof.AllowNetworks("10.0.0.0/8", "127.0.0.1")
if err != nil {
	log.Fatal(err)
}
```

`AllowNetworks` accepts CIDR prefixes and single IPs, IPv4 and IPv6. It checks only `r.RemoteAddr` and ignores `X-Forwarded-For`, because clients can forge that header; behind a reverse proxy it sees the proxy's address.

`BasicAuth` compares credentials in constant time and panics on an empty username or password. Basic auth sends credentials in clear text, so use it over TLS or on a private network.

### Per-caller identity and mTLS

A shared basic auth password does not say *who* took a profile. `Authorize` takes a function that identifies the caller; the returned principal is stored in the request context and reported in `RequestInfo.Principal` for audit logs.

```go
allowed := map[string]bool{"spiffe://corp.example/ns/sre/sa/profiler": true}

srv := pprof.NewServer(pprof.Config{
	Host: "0.0.0.0",
	TLSConfig: &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    corpCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	},
	Middlewares: []pprof.Middleware{pprof.Authorize(func(r *http.Request) (string, error) {
		id, err := pprof.ClientCertPrincipal(r) // SPIFFE ID or certificate CN
		if err != nil {
			return "", err // ErrUnauthenticated -> 401
		}
		if !allowed[id] {
			return id, errors.New("not in the pprof allowlist") // -> 403
		}
		return id, nil
	})},
})
```

- The function returns `pprof.ErrUnauthenticated` (or an error wrapping it) for `401` and any other error for `403`. The error text is never sent to the client.
- `ClientCertPrincipal` returns the SPIFFE ID (a `spiffe://` URI SAN) of the verified client certificate, or its Common Name.
- Instead of mTLS the function can check an SSO token, a service mesh header set by a trusted sidecar, and so on.
- `BasicAuth` also sets the principal, to the username. Custom middleware can call `pprof.WithPrincipal`.

Any `func(http.Handler) http.Handler` works as a middleware.

## Server configuration

```go
pprof.Config{
	Host:                 "127.0.0.1",      // default
	Port:                 6060,             // default
	ReadHeaderTimeout:    10 * time.Second, // default
	IdleTimeout:          60 * time.Second, // default
	ShutdownTimeout:      15 * time.Second, // default
	TLSConfig:            nil,              // set for HTTPS / mTLS
	Middlewares:          nil,
	Limits:               pprof.Limits{},   // safe defaults
	BlockProfileRate:     0,                // 0 = leave runtime setting unchanged
	MutexProfileFraction: 0,                // 0 = leave runtime setting unchanged
	ResetProfileRates:    false,            // true = undo the two settings above on shutdown
}
```

Zero values are replaced with defaults. There is no server-wide `WriteTimeout`, because `/profile` and `/trace` stream for as long as requested; `Guard` sets a per-request write deadline instead. `BlockProfileRate` and `MutexProfileFraction` are process-wide runtime settings applied when the server starts; with `ResetProfileRates` they are undone when `Run`/`Serve` returns (the block rate is set to 0, the mutex fraction is restored). The recommended values (`10_000` ns and `100`) keep the overhead low; `1` records every event and is too expensive for production.

A `Server` is single-use: after shutdown it cannot be started again. Its methods are safe to call from different goroutines.

## Metrics

Prometheus metrics live in a separate module, so the core package keeps no dependencies:

```
go get github.com/jwm1rr0rb10/go-pprof/pprofprom
```

```go
m := pprofprom.MustNew(prometheus.DefaultRegisterer)

limits := pprof.Limits{OnRequest: func(i pprof.RequestInfo) {
	m.Observe(i)
	slog.Info("pprof request", "endpoint", i.Endpoint, "principal", i.Principal, "status", i.Status)
}}
```

| Metric | Type | Labels |
| --- | --- | --- |
| `pprof_requests_total` | counter | `endpoint`, `code`, `rejected` |
| `pprof_request_duration_seconds` | histogram | `endpoint` |
| `pprof_profiling_seconds_total` | counter | `endpoint` |

- All labels are bounded: unknown endpoints become `other`, and `rejected` is a short code such as `busy`, `rate_limited` or `forced_gc`.
- The principal is not a label: it would create a series per caller. Keep it in the audit log.
- `pprof_profiling_seconds_total` shows how long the service has been profiled. An alert on its rate catches a forgotten profiling script.
- To add a prefix or constant labels, wrap the registerer with `prometheus.WrapRegistererWithPrefix` / `WrapRegistererWith`.

## Many instances

All limits are per process. A script that loops over 500 pods can still take 500 CPU profiles at the same moment, one per pod. For fleet-wide limits, route pprof through a proxy or a profiling service that enforces its own concurrency, or use a continuous profiler (see below) instead of ad hoc requests.

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

## Development

```
make race   # tests with the race detector
make cover  # coverage report
make lint   # staticcheck
make bench  # benchmarks
```

## License

[MIT](LICENSE) © Raman Zaitsau
