# Compatibility policy

go-pprof follows [Semantic Versioning](https://semver.org) and the Go module rules. This document states precisely what a version number promises, so that you can upgrade within a major version without reading the code.

## The v1 promise

Code that compiles and works with `github.com/jwm1rr0rb10/go-pprof` v1.N keeps compiling and working with every later v1 release. Breaking changes require a new major version, `github.com/jwm1rr0rb10/go-pprof/v2`, which can be imported side by side with v1.

Every release is checked against the previous tag with [`gorelease`](https://pkg.go.dev/golang.org/x/exp/cmd/gorelease) (`make api`), so an incompatible API change cannot ship in a minor or patch release by accident.

### Never changes within v1

- Exported identifiers are not removed or renamed.
- Function and method signatures do not change.
- Exported struct fields keep their names and types.
- Exported constants and variables keep their meaning. The `Default*` values change only as described under "Stricter safe defaults" below.
- `RequestInfo` stays comparable with `==`.
- The meaning of zero values: a zero field of `Config` or `Limits` always means "use the default", a negative duration or count always means "no limit".
- Paths and names of endpoints (`/debug/pprof/...`, `"heap"`, `"profile"`, ...).
- HTTP status codes for each rejection reason: `404` disabled, `405` method, `403` forced GC / goroutine dump, `429` busy / rate limited (always with `Retry-After`), `401` / `403` authorization, `408` / `413` / `400` request body.

### May change in a minor release (v1.N → v1.N+1)

- New exported functions, types, methods, constants, `Rejected*` reasons.
- **New fields in `Config`, `Limits` and `RequestInfo`.** Use keyed struct literals (`pprof.Limits{MaxConcurrent: 2}`), not positional ones; positional literals are not covered by this policy, as in the Go 1 compatibility promise.
- **Stricter safe defaults.** The purpose of the package is that `Limits{}` is safe for production. When a new kind of expensive request is found, the fix may add a new limit that is on by default (as `MinInterval` in v1.2.0). Every such change:
  - is listed as a behavior change in the release notes;
  - can be turned off with a field that restores the previous behavior (usually a negative value);
  - never happens in a patch release.
- A higher minimum Go version, see below.

### May change in a patch release (v1.N.M → v1.N.M+1)

Only bug fixes and security fixes. A security fix may reject a request that was previously accepted if accepting it was the vulnerability.

### Not covered

- Error texts, response bodies and log messages. Compare errors with `errors.Is` (`ErrUnauthenticated`) and rejections with the `Rejected*` constants, not string literals.
- Unexported identifiers and tests.
- Behavior of `net/http/pprof` and the Go runtime: profile formats, which profiles exist, their cost. The package passes them through.
- Performance numbers, except that the guard adds no work to requests outside `/debug/pprof/`.

## Deprecation

An identifier that should no longer be used gets a `// Deprecated:` comment that names the replacement. It keeps working for the rest of v1 and is removed only in v2.

## Go versions

- The minimum Go version is the one in `go.mod` (currently Go 1.21). CI tests it and the latest stable release.
- It is raised only in a minor release, only when there is a reason (a needed standard library feature or an unfixable security issue), and never above the oldest Go release still supported by the Go team.
- Raising it is listed in the release notes.

## The pprofprom module

`github.com/jwm1rr0rb10/go-pprof/pprofprom` is at v0 and has its own version tags (`pprofprom/v0.N.M`).

- The Go API may change in a minor v0 release; such changes are listed in the release notes.
- **Metric names, label names and label values do not change even in v0**, because renaming a series silently breaks dashboards and alerts. New metrics, and new values of the `rejected` label for new rejection reasons, may be added.
- It moves to v1, with the same promise as the core module, once the API has been stable in production use.
