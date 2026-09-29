package pprof

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Default limits applied by [Guard] (and by [Server]) when the corresponding
// [Limits] field is zero.
const (
	DefaultMaxProfileDuration = 60 * time.Second
	DefaultMaxTraceDuration   = 10 * time.Second
	DefaultMaxConcurrent      = 4
	DefaultWriteTimeout       = 30 * time.Second
)

// Defaults of net/http/pprof when "seconds" is missing or invalid.
const (
	stdProfileDuration = 30 * time.Second
	stdTraceDuration   = 1 * time.Second
)

// Limits protect a production service from expensive or careless
// requests to pprof endpoints. The zero value is a safe configuration:
// see the Default* constants and the field comments.
type Limits struct {
	// MaxProfileDuration caps "seconds" of /profile and of delta profiles
	// (/heap?seconds=N, /allocs?seconds=N, ...). Longer requests are clamped,
	// not rejected. 0 means DefaultMaxProfileDuration (60s); negative means
	// no limit.
	MaxProfileDuration time.Duration

	// MaxTraceDuration caps "seconds" of /trace. An execution trace is
	// heavier than a CPU profile and grows by megabytes per second on a
	// busy service. 0 means DefaultMaxTraceDuration (10s); negative means no
	// limit.
	MaxTraceDuration time.Duration

	// MaxConcurrent limits the number of pprof requests served at the same
	// time. Extra requests get 429 Too Many Requests instead of queueing.
	// Independently of this limit, at most one /profile and one /trace run
	// at a time (the runtime supports only one of each anyway).
	// 0 means DefaultMaxConcurrent (4); negative means no limit.
	MaxConcurrent int

	// WriteTimeout is how long the response may take to write after the
	// profile has been collected. It frees slots held by slow or stuck
	// clients. The write deadline of a request is its collection time
	// (e.g. the clamped "seconds") plus WriteTimeout.
	// 0 means DefaultWriteTimeout (30s); negative means no deadline.
	WriteTimeout time.Duration

	// AllowForcedGC allows /heap?gc=1, which runs a full garbage collection
	// before taking the profile. Rejected with 403 by default, because a
	// script calling it in a loop visibly slows down a busy service.
	AllowForcedGC bool

	// AllowFullGoroutineDump allows /goroutine?debug=2, a full text dump of
	// every goroutine. With hundreds of thousands of goroutines it pauses
	// the program and produces a very large response. Rejected with 403 by
	// default; /goroutine and /goroutine?debug=1 (aggregated stacks) always
	// work.
	AllowFullGoroutineDump bool

	// DisabledEndpoints lists endpoints that return 404, by name: "index",
	// "cmdline", "profile", "symbol", "trace", or a profile name such as
	// "heap", "goroutine", "allocs", "block", "mutex", "threadcreate".
	// Disabling "cmdline" is a good idea if secrets are passed as flags.
	DisabledEndpoints []string

	// OnRequest, if set, is called after every request to a pprof endpoint,
	// including rejected ones. Use it for audit logs and metrics. It runs
	// synchronously on the request goroutine, so keep it fast.
	OnRequest func(RequestInfo)
}

// RequestInfo describes a finished request to a pprof endpoint.
type RequestInfo struct {
	Endpoint   string        // "index", "profile", "heap", ...
	Method     string        // HTTP method
	RemoteAddr string        // r.RemoteAddr
	Status     int           // HTTP status code sent to the client
	Duration   time.Duration // time spent serving the request
	Seconds    float64       // effective "seconds" after clamping, 0 if not applicable
	Rejected   string        // why the guard rejected the request, "" if it was served
}

// Reasons reported in RequestInfo.Rejected.
const (
	RejectedDisabled      = "endpoint disabled"
	RejectedMethod        = "method not allowed"
	RejectedForcedGC      = "forced GC not allowed"
	RejectedGoroutineDump = "full goroutine dump not allowed"
	RejectedBusy          = "too many concurrent requests"
)

func (l Limits) withDefaults() Limits {
	if l.MaxProfileDuration == 0 {
		l.MaxProfileDuration = DefaultMaxProfileDuration
	}
	if l.MaxTraceDuration == 0 {
		l.MaxTraceDuration = DefaultMaxTraceDuration
	}
	if l.MaxConcurrent == 0 {
		l.MaxConcurrent = DefaultMaxConcurrent
	}
	if l.WriteTimeout == 0 {
		l.WriteTimeout = DefaultWriteTimeout
	}
	return l
}

// Guard returns a Middleware that enforces l on pprof endpoints. [Server]
// always applies it with Config.Limits. When mounting pprof on your own mux
// with [Register], pass it explicitly, after authentication middleware so
// that unauthenticated requests do not take concurrency slots:
//
//	pprof.Register(mux, pprof.BasicAuth(user, pass), pprof.Guard(pprof.Limits{}))
//
// Requests to paths outside /debug/pprof/ are passed through unchanged.
func Guard(l Limits) Middleware {
	l = l.withDefaults()

	disabled := make(map[string]bool, len(l.DisabledEndpoints))
	for _, name := range l.DisabledEndpoints {
		disabled[name] = true
	}

	var all semaphore
	if l.MaxConcurrent > 0 {
		all = make(semaphore, l.MaxConcurrent)
	}
	cpu, trace := make(semaphore, 1), make(semaphore, 1)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name, ok := endpointName(r.URL.Path)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			info := RequestInfo{Endpoint: name, Method: r.Method, RemoteAddr: r.RemoteAddr}
			defer func() {
				if l.OnRequest != nil {
					info.Status = rec.statusOrOK()
					info.Duration = time.Since(start)
					l.OnRequest(info)
				}
			}()

			reject := func(status int, reason string) {
				info.Rejected = reason
				if status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "5")
				}
				if status == http.StatusMethodNotAllowed {
					w.Header().Set("Allow", allowedMethods(name))
				}
				http.Error(rec, reason, status)
			}

			if disabled[name] {
				reject(http.StatusNotFound, RejectedDisabled)
				return
			}
			// Only /symbol takes a body. Parameters of every other endpoint are
			// read with FormValue, which also reads POST bodies, so accepting
			// POST would let "seconds" bypass the checks below.
			if !methodAllowed(name, r.Method) {
				reject(http.StatusMethodNotAllowed, RejectedMethod)
				return
			}

			q := r.URL.Query()
			if name == "heap" && !l.AllowForcedGC && atoi(q.Get("gc")) > 0 {
				reject(http.StatusForbidden, RejectedForcedGC)
				return
			}
			if name == "goroutine" && !l.AllowFullGoroutineDump && atoi(q.Get("debug")) >= 2 {
				reject(http.StatusForbidden, RejectedGoroutineDump)
				return
			}

			collect, clamped := clampSeconds(name, q, l)
			info.Seconds = collect.Seconds()
			if clamped {
				r = r.Clone(r.Context())
				r.URL.RawQuery = q.Encode()
			}

			if !all.tryAcquire() {
				reject(http.StatusTooManyRequests, RejectedBusy)
				return
			}
			defer all.release()

			if exclusive := exclusiveSem(name, cpu, trace); exclusive != nil {
				if !exclusive.tryAcquire() {
					reject(http.StatusTooManyRequests, RejectedBusy)
					return
				}
				defer exclusive.release()
			}

			if l.WriteTimeout > 0 {
				// Errors are ignored: not every ResponseWriter supports deadlines
				// (httptest.ResponseRecorder, some wrappers), and the limit is a
				// safety net, not a requirement.
				_ = http.NewResponseController(w).SetWriteDeadline(start.Add(collect + l.WriteTimeout))
			}

			next.ServeHTTP(rec, r)
		})
	}
}

// endpointName maps a request path to an endpoint name. ok is false for
// paths outside PathPrefix.
func endpointName(path string) (name string, ok bool) {
	rest, ok := strings.CutPrefix(path, PathPrefix)
	if !ok {
		return "", false
	}
	if rest == "" {
		return "index", true
	}
	return rest, true
}

func methodAllowed(name, method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodPost:
		return name == "symbol"
	default:
		return false
	}
}

func allowedMethods(name string) string {
	if name == "symbol" {
		return "GET, HEAD, POST"
	}
	return "GET, HEAD"
}

func exclusiveSem(name string, cpu, trace semaphore) semaphore {
	switch name {
	case "profile":
		return cpu
	case "trace":
		return trace
	default:
		return nil
	}
}

// clampSeconds returns how long the request will collect data and whether
// q was changed to enforce the limit. It mirrors how net/http/pprof parses
// "seconds": /profile takes an integer (default 30), /trace a float
// (default 1), delta profiles a positive integer (no default; an invalid
// value is rejected by net/http/pprof itself, so it is left alone).
func clampSeconds(name string, q url.Values, l Limits) (collect time.Duration, changed bool) {
	raw := q.Get("seconds")

	switch name {
	case "profile":
		d := stdProfileDuration
		if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 0 {
			d = secondsToDuration(float64(sec))
		}
		return clampInt(q, d, l.MaxProfileDuration)

	case "trace":
		d := stdTraceDuration
		if sec, err := strconv.ParseFloat(raw, 64); err == nil && sec > 0 {
			d = secondsToDuration(sec)
		}
		if l.MaxTraceDuration > 0 && d > l.MaxTraceDuration {
			q.Set("seconds", strconv.FormatFloat(l.MaxTraceDuration.Seconds(), 'f', -1, 64))
			return l.MaxTraceDuration, true
		}
		return d, false

	default: // index, cmdline, symbol and named (possibly delta) profiles
		if raw == "" || name == "index" || name == "cmdline" || name == "symbol" {
			return 0, false
		}
		sec, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || sec <= 0 {
			return 0, false
		}
		return clampInt(q, secondsToDuration(float64(sec)), l.MaxProfileDuration)
	}
}

// clampInt caps d at max (if max > 0), writing whole seconds (at least 1)
// to q because /profile and delta profiles only accept integers.
func clampInt(q url.Values, d, max time.Duration) (time.Duration, bool) {
	if max <= 0 || d <= max {
		return d, false
	}
	sec := int64(max / time.Second)
	if sec < 1 {
		sec = 1
	}
	q.Set("seconds", strconv.FormatInt(sec, 10))
	return time.Duration(sec) * time.Second, true
}

func secondsToDuration(sec float64) time.Duration {
	if sec >= float64(math.MaxInt64)/float64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(sec * float64(time.Second))
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// semaphore is a non-blocking counting semaphore; a nil semaphore has no
// limit.
type semaphore chan struct{}

func (s semaphore) tryAcquire() bool {
	if s == nil {
		return true
	}
	select {
	case s <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s semaphore) release() {
	if s != nil {
		<-s
	}
}

// statusRecorder captures the status code for RequestInfo.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) statusOrOK() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
