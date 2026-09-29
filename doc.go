// Package pprof provides a small, safe wrapper around net/http/pprof.
//
// It offers two ways to expose profiling endpoints:
//
//   - [Register] mounts all pprof handlers on an existing [http.ServeMux].
//   - [Server] runs a standalone pprof HTTP server (on 127.0.0.1:6060 by
//     default) with graceful shutdown driven by a [context.Context].
//
// Both accept optional [Middleware], for example [BasicAuth] and
// [AllowNetworks], to restrict who can access the endpoints.
//
// # Important: side effect of net/http/pprof
//
// This package imports net/http/pprof, whose init function registers all
// pprof handlers on [http.DefaultServeMux]. That cannot be prevented by any
// wrapper. If your application serves [http.DefaultServeMux] (for example
// http.ListenAndServe(addr, nil) or http.Handle(...)), pprof will be
// reachable on that server too. Always use your own [http.ServeMux] for
// public servers.
package pprof
