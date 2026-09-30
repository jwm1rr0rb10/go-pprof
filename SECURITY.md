# Security policy

go-pprof exists to make `net/http/pprof` safe to run on production services, so security reports are taken seriously and handled before feature work.

## Reporting a vulnerability

**Do not open a public issue.** Report privately through GitHub:

1. Open <https://github.com/jwm1rr0rb10/go-pprof/security/advisories/new> (Security → Report a vulnerability).
2. Describe the affected version, the configuration (`Config` / `Limits` / middlewares), the request or steps that trigger the problem, and the impact.

A minimal Go program or `curl` command that reproduces the issue is the fastest way to a fix.

## What to expect

| Step | Target |
| --- | --- |
| Acknowledgement of the report | 3 working days |
| Initial assessment (confirmed / not a vulnerability / need more data) | 7 days |
| Fix released for a confirmed issue | 30 days; 90 days at most for complex cases |

The reporter is kept informed, can review the fix before release, and is credited in the advisory unless they prefer otherwise. Please give us the time above before disclosing the issue publicly.

Fixes are published as a GitHub Security Advisory, a patch release, and a report to the [Go vulnerability database](https://pkg.go.dev/vuln/) so that `govulncheck` users are notified.

## Supported versions

| Module | Version | Security fixes |
| --- | --- | --- |
| `github.com/jwm1rr0rb10/go-pprof` | latest minor (`v1.3.x`) | yes |
| | previous minor | critical and high severity, for 6 months after the next minor is released |
| | older | no, upgrade (upgrades within v1 are compatible, see [COMPATIBILITY.md](COMPATIBILITY.md)) |
| `github.com/jwm1rr0rb10/go-pprof/pprofprom` | latest `v0.x` | yes |

## Scope

In scope: any way to defeat what the package promises, for example

- bypassing a `Limits` check: duration caps, `MaxConcurrent`, the single `/profile` / `/trace` slot, `MinInterval`, the forced GC or full goroutine dump rules, `DisabledEndpoints`, body size or read timeout of `/symbol`;
- bypassing `BasicAuth`, `AllowNetworks` or `Authorize`, or getting a wrong `RequestInfo.Principal`;
- leaking credentials or `AuthorizeFunc` error text to the client;
- a request pattern that makes the guard itself use unbounded memory, goroutines or time;
- leaked concurrency slots or other state that keeps pprof unavailable after the requests end;
- unbounded label cardinality in `pprofprom`.

Out of scope (please report elsewhere or not at all):

- vulnerabilities in the Go standard library, including `net/http/pprof` itself: report them to the [Go security team](https://go.dev/doc/security/policy);
- the documented side effect that importing `net/http/pprof` registers handlers on `http.DefaultServeMux`;
- insecure configurations the documentation warns against: listening on a public address without authentication, basic auth over plain HTTP, disabling limits with negative values, `AllowForcedGC` / `AllowFullGoroutineDump` enabled;
- the cost of profiling that the configured limits allow (e.g. one 60 s CPU profile is expected overhead);
- limits not being shared across processes (they are per process by design, see "Many instances" in the README).

## Hardening checklist for users

- Keep pprof on `127.0.0.1` or behind mTLS (`Config.TLSConfig` + `Authorize(ClientCertPrincipal)`).
- Never serve `http.DefaultServeMux` on a public listener.
- Disable `cmdline` if secrets are passed as flags.
- Log `OnRequest` with `Principal` for an audit trail.
- Build with a supported, patched Go release and run `govulncheck ./...`: most HTTP and TLS vulnerabilities are fixed in the standard library, not here.
