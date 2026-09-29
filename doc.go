// Package pprof exposes net/http/pprof safely on production services.
//
// It offers two ways to expose profiling endpoints:
//
//   - [Server] runs a standalone pprof HTTP server (on 127.0.0.1:6060 by
//     default) with graceful shutdown driven by a [context.Context]. It
//     always applies [Limits].
//   - [Register] mounts the raw pprof handlers on an existing
//     [http.ServeMux]; add [Guard] to apply [Limits] there too.
//
// # Limits
//
// Some pprof requests are expensive on a busy service: a long CPU profile
// or execution trace, a forced GC (/heap?gc=1), a full goroutine dump
// (/goroutine?debug=2). [Guard] caps profile and trace durations, limits
// concurrent requests (and allows only one CPU profile and one trace at a
// time), rejects the expensive parameters unless allowed, sets a
// per-request write deadline so slow clients cannot hold slots, and reports
// every request to an optional hook for audit logs and metrics. The zero
// [Limits] value is a safe production configuration.
//
// # Access control
//
// [BasicAuth] and [AllowNetworks] restrict who can reach the endpoints.
// Any func(http.Handler) http.Handler can be used as a [Middleware].
//
// # Important: side effect of net/http/pprof
//
// This package imports net/http/pprof, whose init function registers all
// pprof handlers on [http.DefaultServeMux]. That cannot be prevented by any
// wrapper. If your application serves [http.DefaultServeMux] (for example
// http.ListenAndServe(addr, nil) or http.Handle(...)), pprof will be
// reachable on that server too, without limits. Always use your own
// [http.ServeMux] for public servers.
package pprof
