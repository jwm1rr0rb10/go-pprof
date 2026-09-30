package pprof

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Default limits applied by [Guard] (and by [Server]) when the corresponding
// [Limits] field is zero.
const (
	DefaultMaxProfileDuration = 60 * time.Second
	DefaultMaxTraceDuration   = 10 * time.Second
	DefaultMaxConcurrent      = 4
	DefaultWriteTimeout       = 30 * time.Second
	DefaultReadTimeout        = 10 * time.Second
	DefaultMinInterval        = 1 * time.Second
	DefaultMaxSymbolBodyBytes = 1 << 20
)

// Defaults of net/http/pprof when "seconds" is missing or invalid.
const (
	stdProfileDuration = 30 * time.Second
	stdTraceDuration   = 1 * time.Second
)

// busyRetryAfter is the Retry-After sent when MaxConcurrent is reached: the
// guard cannot know when the running requests will finish.
const busyRetryAfter = 5 * time.Second

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

	// MinInterval is the minimum time between two requests to the same
	// profile endpoint (/heap, /goroutine, /profile, ...). Each endpoint has
	// its own clock, so /heap and /goroutine can be taken back to back, but
	// a script polling /goroutine in a loop gets 429 with an exact
	// Retry-After. The cheap endpoints (index, cmdline, symbol) are not
	// limited. 0 means DefaultMinInterval (1s); negative means no limit.
	MinInterval time.Duration

	// WriteTimeout is how long the response may take to write after the
	// profile has been collected. It frees slots held by slow or stuck
	// clients. The write deadline of a request is its collection time
	// (e.g. the clamped "seconds") plus WriteTimeout.
	// 0 means DefaultWriteTimeout (30s); negative means no deadline.
	WriteTimeout time.Duration

	// ReadTimeout is how long a client may take to send the body of
	// POST /symbol, the only endpoint that reads one. Slower clients get
	// 408 and free their slot. 0 means DefaultReadTimeout (10s); negative
	// means no deadline.
	ReadTimeout time.Duration

	// MaxSymbolBodyBytes caps the body of POST /symbol. Larger bodies get
	// 413. 0 means DefaultMaxSymbolBodyBytes (1 MiB, tens of thousands of
	// addresses); negative means no limit.
	MaxSymbolBodyBytes int64

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
	Principal  string        // caller identity set by Authorize, BasicAuth or WithPrincipal; "" if none
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
	RejectedRateLimited   = "requested too often"
	RejectedBodyTooLarge  = "request body too large"
	RejectedReadTimeout   = "request body read timeout"
	RejectedBadBody       = "cannot read request body"
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
	if l.MinInterval == 0 {
		l.MinInterval = DefaultMinInterval
	}
	if l.WriteTimeout == 0 {
		l.WriteTimeout = DefaultWriteTimeout
	}
	if l.ReadTimeout == 0 {
		l.ReadTimeout = DefaultReadTimeout
	}
	if l.MaxSymbolBodyBytes == 0 {
		l.MaxSymbolBodyBytes = DefaultMaxSymbolBodyBytes
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
	cpu, trace := newExclusive(), newExclusive()
	rate := newRateLimiter(l.MinInterval)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name, ok := endpointName(r.URL.Path)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			info := RequestInfo{
				Endpoint:   name,
				Method:     r.Method,
				RemoteAddr: r.RemoteAddr,
				Principal:  PrincipalFromContext(r.Context()),
			}
			defer func() {
				if l.OnRequest != nil {
					info.Status = rec.statusOrOK()
					info.Duration = time.Since(start)
					l.OnRequest(info)
				}
			}()

			reject := func(status int, reason string) {
				info.Rejected = reason
				if status == http.StatusMethodNotAllowed {
					w.Header().Set("Allow", allowedMethods(name))
				}
				http.Error(rec, reason, status)
			}
			tooMany := func(reason string, retryAfter time.Duration) {
				w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
				reject(http.StatusTooManyRequests, reason)
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
				tooMany(RejectedBusy, busyRetryAfter)
				return
			}
			defer all.release()

			if ex := exclusiveFor(name, cpu, trace); ex != nil {
				if wait, ok := ex.tryAcquire(start.Add(collect)); !ok {
					tooMany(RejectedBusy, wait)
					return
				}
				defer ex.release()
			}

			// Checked after the semaphores so that a request rejected as busy
			// does not use up the interval.
			if wait, ok := rate.allow(name, start); !ok {
				tooMany(RejectedRateLimited, wait)
				return
			}

			if name == "symbol" && r.Method == http.MethodPost {
				var status int
				var reason string
				if r, status, reason = bufferBody(w, r, start, l); status != 0 {
					reject(status, reason)
					return
				}
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

func exclusiveFor(name string, cpu, trace *exclusive) *exclusive {
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

// retryAfterSeconds formats d for the Retry-After header: whole seconds,
// rounded up, at least 1.
func retryAfterSeconds(d time.Duration) string {
	sec := int64((d + time.Second - 1) / time.Second)
	if sec < 1 {
		sec = 1
	}
	return strconv.FormatInt(sec, 10)
}

// bufferBody reads the body of POST /symbol into memory, enforcing
// l.MaxSymbolBodyBytes and l.ReadTimeout, and returns a request whose body
// is the buffered copy. net/http/pprof would otherwise read the body for
// as long as the client keeps sending it, holding a concurrency slot. A
// non-zero status means the request must be rejected with reason.
func bufferBody(w http.ResponseWriter, r *http.Request, start time.Time, l Limits) (_ *http.Request, status int, reason string) {
	rc := http.NewResponseController(w)
	fail := func(status int, reason string) (*http.Request, int, string) {
		// The rest of the body is not read: close the connection, and make
		// sure net/http does not wait for the body before replying.
		// Errors are ignored for the same reason as SetWriteDeadline.
		w.Header().Set("Connection", "close")
		_ = rc.SetReadDeadline(time.Now())
		return r, status, reason
	}

	if l.MaxSymbolBodyBytes > 0 && r.ContentLength > l.MaxSymbolBodyBytes {
		return fail(http.StatusRequestEntityTooLarge, RejectedBodyTooLarge)
	}
	if l.ReadTimeout > 0 {
		_ = rc.SetReadDeadline(start.Add(l.ReadTimeout))
	}

	body := r.Body
	if l.MaxSymbolBodyBytes > 0 {
		// w, not a wrapper: MaxBytesReader tells the server to close the
		// connection through the original ResponseWriter.
		body = http.MaxBytesReader(w, body, l.MaxSymbolBodyBytes)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		var netErr net.Error
		switch {
		case errors.As(err, &tooLarge):
			return fail(http.StatusRequestEntityTooLarge, RejectedBodyTooLarge)
		case errors.As(err, &netErr) && netErr.Timeout():
			return fail(http.StatusRequestTimeout, RejectedReadTimeout)
		default:
			return fail(http.StatusBadRequest, RejectedBadBody)
		}
	}
	if l.ReadTimeout > 0 {
		_ = rc.SetReadDeadline(time.Time{})
	}

	r = r.Clone(r.Context())
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	return r, 0, ""
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

// exclusive allows one request at a time and remembers when the running
// one is expected to finish, for an accurate Retry-After.
type exclusive struct {
	sem   semaphore
	until atomic.Int64 // UnixNano when the current holder finishes collecting
}

func newExclusive() *exclusive { return &exclusive{sem: make(semaphore, 1)} }

// tryAcquire takes the slot until the given time. If the slot is busy it
// returns how long the current holder still needs.
func (e *exclusive) tryAcquire(until time.Time) (wait time.Duration, ok bool) {
	if e.sem.tryAcquire() {
		e.until.Store(until.UnixNano())
		return 0, true
	}
	return time.Until(time.Unix(0, e.until.Load())), false
}

func (e *exclusive) release() { e.sem.release() }

// cheapEndpoints are never rate limited.
var cheapEndpoints = map[string]bool{"index": true, "cmdline": true, "symbol": true}

// rateLimiter enforces a minimum interval between requests to the same
// endpoint. A nil rateLimiter allows everything.
type rateLimiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     map[string]time.Time // earliest time the endpoint may be requested again
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	if interval <= 0 {
		return nil
	}
	return &rateLimiter{interval: interval, next: make(map[string]time.Time)}
}

func (rl *rateLimiter) allow(name string, now time.Time) (wait time.Duration, ok bool) {
	if rl == nil || cheapEndpoints[name] {
		return 0, true
	}
	// Only track endpoints that exist, so that requests to random paths
	// cannot grow the map. Unknown names get 404 from net/http/pprof anyway.
	if name != "profile" && name != "trace" && pprof.Lookup(name) == nil {
		return 0, true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if next := rl.next[name]; now.Before(next) {
		return next.Sub(now), false
	}
	rl.next[name] = now.Add(rl.interval)
	return 0, true
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
