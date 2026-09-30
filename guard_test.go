package pprof

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// seenQuery returns a handler that records the query it received.
func seenQuery(got *url.Values) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	})
}

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

func TestGuardClampsSeconds(t *testing.T) {
	limits := Limits{MaxProfileDuration: 10 * time.Second, MaxTraceDuration: 2500 * time.Millisecond}

	tests := []struct {
		target string
		want   string // expected "seconds" seen by the handler; "-" means absent
	}{
		{"/debug/pprof/profile?seconds=3600", "10"},
		{"/debug/pprof/profile?seconds=5", "5"},
		{"/debug/pprof/profile", "10"},             // stdlib default 30s > max
		{"/debug/pprof/profile?seconds=abc", "10"}, // stdlib treats invalid as 30s
		{"/debug/pprof/profile?seconds=-1", "10"},
		{"/debug/pprof/trace?seconds=600", "2.5"},
		{"/debug/pprof/trace?seconds=0.5", "0.5"},
		{"/debug/pprof/trace", "-"}, // stdlib default 1s < max
		{"/debug/pprof/heap?seconds=3600", "10"},
		{"/debug/pprof/allocs?seconds=3", "3"},
		{"/debug/pprof/heap", "-"},
		{"/debug/pprof/heap?seconds=bad", "bad"}, // left for net/http/pprof to reject
		{"/debug/pprof/profile?seconds=3600&seconds=1", "10"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			var got url.Values
			rr := serve(Guard(limits)(seenQuery(&got)), http.MethodGet, tt.target)
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d", rr.Code)
			}
			vals, ok := got["seconds"]
			switch {
			case tt.want == "-" && ok:
				t.Errorf("seconds = %v, want absent", vals)
			case tt.want != "-" && (len(vals) != 1 || vals[0] != tt.want):
				t.Errorf("seconds = %v, want [%s]", vals, tt.want)
			}
		})
	}
}

func TestGuardSubSecondProfileMax(t *testing.T) {
	var got url.Values
	serve(Guard(Limits{MaxProfileDuration: 300 * time.Millisecond})(seenQuery(&got)), http.MethodGet, "/debug/pprof/profile")
	if got.Get("seconds") != "1" {
		t.Errorf("seconds = %q, want 1 (profile only accepts whole seconds)", got.Get("seconds"))
	}
}

func TestGuardNoLimits(t *testing.T) {
	var got url.Values
	h := Guard(Limits{MaxProfileDuration: -1, MaxTraceDuration: -1})(seenQuery(&got))
	serve(h, http.MethodGet, "/debug/pprof/profile?seconds=3600")
	if got.Get("seconds") != "3600" {
		t.Errorf("profile seconds = %q, want unchanged", got.Get("seconds"))
	}
	serve(h, http.MethodGet, "/debug/pprof/trace?seconds=600")
	if got.Get("seconds") != "600" {
		t.Errorf("trace seconds = %q, want unchanged", got.Get("seconds"))
	}
}

func TestGuardDoesNotMutateOriginalRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/profile?seconds=3600", nil)
	Guard(Limits{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	if req.URL.RawQuery != "seconds=3600" {
		t.Errorf("original request changed: %q", req.URL.RawQuery)
	}
}

func TestGuardMethods(t *testing.T) {
	h := Guard(Limits{})(okHandler)
	tests := []struct {
		method, target string
		want           int
	}{
		{http.MethodGet, "/debug/pprof/profile", http.StatusOK},
		{http.MethodHead, "/debug/pprof/heap", http.StatusOK},
		{http.MethodPost, "/debug/pprof/profile", http.StatusMethodNotAllowed},
		{http.MethodPost, "/debug/pprof/trace", http.StatusMethodNotAllowed},
		{http.MethodPut, "/debug/pprof/heap", http.StatusMethodNotAllowed},
		{http.MethodPost, "/debug/pprof/symbol", http.StatusOK},
	}
	for _, tt := range tests {
		rr := serve(h, tt.method, tt.target)
		if rr.Code != tt.want {
			t.Errorf("%s %s: status %d, want %d", tt.method, tt.target, rr.Code, tt.want)
		}
		if rr.Code == http.StatusMethodNotAllowed && rr.Header().Get("Allow") == "" {
			t.Errorf("%s %s: missing Allow header", tt.method, tt.target)
		}
	}
}

// A POST body must not be able to smuggle "seconds" past the clamp.
func TestGuardPostBodyCannotBypassClamp(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/debug/pprof/profile", strings.NewReader("seconds=3600"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	Guard(Limits{})(okHandler).ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rr.Code)
	}
}

func TestGuardExpensiveParameters(t *testing.T) {
	strict := Guard(Limits{MinInterval: -1})(okHandler)
	relaxed := Guard(Limits{MinInterval: -1, AllowForcedGC: true, AllowFullGoroutineDump: true})(okHandler)

	tests := []struct {
		target      string
		strict, lax int
	}{
		{"/debug/pprof/heap?gc=1", http.StatusForbidden, http.StatusOK},
		{"/debug/pprof/heap?gc=0", http.StatusOK, http.StatusOK},
		{"/debug/pprof/heap", http.StatusOK, http.StatusOK},
		{"/debug/pprof/goroutine?debug=2", http.StatusForbidden, http.StatusOK},
		{"/debug/pprof/goroutine?debug=3", http.StatusForbidden, http.StatusOK},
		{"/debug/pprof/goroutine?debug=1", http.StatusOK, http.StatusOK},
		{"/debug/pprof/goroutine", http.StatusOK, http.StatusOK},
		{"/debug/pprof/allocs?gc=1", http.StatusOK, http.StatusOK}, // stdlib forces GC only for heap
	}
	for _, tt := range tests {
		if got := serve(strict, http.MethodGet, tt.target).Code; got != tt.strict {
			t.Errorf("strict %s: status %d, want %d", tt.target, got, tt.strict)
		}
		if got := serve(relaxed, http.MethodGet, tt.target).Code; got != tt.lax {
			t.Errorf("relaxed %s: status %d, want %d", tt.target, got, tt.lax)
		}
	}
}

func TestGuardDisabledEndpoints(t *testing.T) {
	h := Guard(Limits{DisabledEndpoints: []string{"cmdline", "trace", "index"}})(okHandler)
	for target, want := range map[string]int{
		"/debug/pprof/cmdline": http.StatusNotFound,
		"/debug/pprof/trace":   http.StatusNotFound,
		"/debug/pprof/":        http.StatusNotFound,
		"/debug/pprof/heap":    http.StatusOK,
		"/debug/pprof/profile": http.StatusOK,
	} {
		if got := serve(h, http.MethodGet, target).Code; got != want {
			t.Errorf("%s: status %d, want %d", target, got, want)
		}
	}
}

func TestGuardPassesThroughOtherPaths(t *testing.T) {
	h := Guard(Limits{DisabledEndpoints: []string{"index"}})(okHandler)
	if got := serve(h, http.MethodPost, "/api/users").Code; got != http.StatusOK {
		t.Errorf("status %d, want 200", got)
	}
}

// blockingHandler blocks every request until release is closed.
func blockingHandler() (h http.Handler, entered <-chan struct{}, release func()) {
	in := make(chan struct{}, 100)
	done := make(chan struct{})
	var once sync.Once
	h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		in <- struct{}{}
		<-done
		w.WriteHeader(http.StatusOK)
	})
	return h, in, func() { once.Do(func() { close(done) }) }
}

func TestGuardMaxConcurrent(t *testing.T) {
	inner, entered, release := blockingHandler()
	defer release()
	h := Guard(Limits{MaxConcurrent: 2, MinInterval: -1})(inner)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); serve(h, http.MethodGet, "/debug/pprof/heap") }()
		<-entered
	}

	rr := serve(h, http.MethodGet, "/debug/pprof/goroutine")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("third request: status %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header")
	}

	release()
	wg.Wait()
	if got := serve(h, http.MethodGet, "/debug/pprof/heap").Code; got != http.StatusOK {
		t.Fatalf("after release: status %d, want 200", got)
	}
}

func TestGuardOneProfileAndOneTraceAtATime(t *testing.T) {
	inner, entered, release := blockingHandler()
	defer release()
	h := Guard(Limits{MaxConcurrent: 10})(inner)

	var wg sync.WaitGroup
	for _, target := range []string{"/debug/pprof/profile?seconds=1", "/debug/pprof/trace?seconds=1"} {
		wg.Add(1)
		go func(target string) { defer wg.Done(); serve(h, http.MethodGet, target) }(target)
		<-entered
	}

	for _, target := range []string{"/debug/pprof/profile", "/debug/pprof/trace"} {
		if got := serve(h, http.MethodGet, target).Code; got != http.StatusTooManyRequests {
			t.Errorf("second %s: status %d, want 429", target, got)
		}
	}
	// Other endpoints are still served.
	go func() { serve(h, http.MethodGet, "/debug/pprof/heap") }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("heap request was blocked by running profile/trace")
	}
	release()
	wg.Wait()
}

func TestGuardUnlimitedConcurrency(t *testing.T) {
	inner, entered, release := blockingHandler()
	defer release()
	h := Guard(Limits{MaxConcurrent: -1, MinInterval: -1})(inner)

	for i := 0; i < 20; i++ {
		go serve(h, http.MethodGet, "/debug/pprof/heap")
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("request %d was limited", i)
		}
	}
}

func TestGuardOnRequest(t *testing.T) {
	var mu sync.Mutex
	var infos []RequestInfo
	h := Guard(Limits{
		MaxProfileDuration: 5 * time.Second,
		OnRequest: func(i RequestInfo) {
			mu.Lock()
			infos = append(infos, i)
			mu.Unlock()
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	serve(h, http.MethodGet, "/debug/pprof/profile?seconds=60")
	serve(h, http.MethodGet, "/debug/pprof/heap?gc=1")
	serve(h, http.MethodGet, "/other") // not a pprof endpoint: no callback

	if len(infos) != 2 {
		t.Fatalf("got %d callbacks, want 2: %+v", len(infos), infos)
	}
	ok := infos[0]
	if ok.Endpoint != "profile" || ok.Status != http.StatusAccepted || ok.Seconds != 5 || ok.Rejected != "" || ok.Method != http.MethodGet || ok.RemoteAddr == "" {
		t.Errorf("served request info = %+v", ok)
	}
	rej := infos[1]
	if rej.Endpoint != "heap" || rej.Status != http.StatusForbidden || rej.Rejected != RejectedForcedGC {
		t.Errorf("rejected request info = %+v", rej)
	}
}

func TestGuardAllRealEndpointsWithDefaults(t *testing.T) {
	h := Handler(Guard(Limits{}))
	for _, ep := range endpoints {
		if got := serve(h, http.MethodGet, ep).Code; got != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", ep, got)
		}
	}
}

// End to end: a real server clamps a one-hour CPU profile to one second.
func TestServerClampsRealProfile(t *testing.T) {
	ln := localListener(t)
	s := NewServer(Config{Limits: Limits{MaxProfileDuration: time.Second}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx, ln) }()

	base := "http://" + ln.Addr().String()
	waitReady(t, base+"/debug/pprof/")

	start := time.Now()
	resp, err := http.Get(base + "/debug/pprof/profile?seconds=3600")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("status %d, %d bytes", resp.StatusCode, len(body))
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("profile took %v, clamp not applied", elapsed)
	}

	// Default limits are active on the server: forced GC is rejected.
	resp, err = http.Get(base + "/debug/pprof/heap?gc=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("heap?gc=1: status %d, want 403", resp.StatusCode)
	}
}

func TestServerAppliesContentionSettings(t *testing.T) {
	prev := runtime.SetMutexProfileFraction(-1)
	defer runtime.SetMutexProfileFraction(prev)
	defer runtime.SetBlockProfileRate(0)

	s := NewServer(Config{
		BlockProfileRate:     RecommendedBlockProfileRate,
		MutexProfileFraction: RecommendedMutexProfileFraction,
	})
	_ = s.Shutdown(context.Background()) // Serve applies settings, then returns at once
	if err := s.Serve(context.Background(), localListener(t)); err != nil {
		t.Fatal(err)
	}
	if got := runtime.SetMutexProfileFraction(-1); got != RecommendedMutexProfileFraction {
		t.Errorf("mutex profile fraction = %d, want %d", got, RecommendedMutexProfileFraction)
	}
}

func TestServerTimeoutsConfigured(t *testing.T) {
	s := NewServer(Config{})
	if s.srv.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", s.srv.IdleTimeout, DefaultIdleTimeout)
	}
	if s.srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (per-request deadlines are used)", s.srv.WriteTimeout)
	}
	if s.srv.MaxHeaderBytes != maxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d, want %d", s.srv.MaxHeaderBytes, maxHeaderBytes)
	}
}

// Server middlewares run before the guard: unauthorized requests must not
// take concurrency slots.
func TestServerAuthBeforeGuard(t *testing.T) {
	var calls int
	s := NewServer(Config{
		Middlewares: []Middleware{BasicAuth("u", "p")},
		Limits:      Limits{OnRequest: func(RequestInfo) { calls++ }},
	})
	rr := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/debug/pprof/heap", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rr.Code)
	}
	if calls != 0 {
		t.Fatalf("guard ran for an unauthenticated request")
	}
}

// deadlineWriter records the write deadline set via http.ResponseController.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.deadline = t
	return nil
}

func TestGuardSetsWriteDeadline(t *testing.T) {
	tests := []struct {
		target string
		limits Limits
		want   time.Duration // expected deadline relative to the request start; 0 = none
	}{
		{"/debug/pprof/profile?seconds=3600", Limits{MaxProfileDuration: 10 * time.Second, WriteTimeout: 5 * time.Second}, 15 * time.Second},
		{"/debug/pprof/trace?seconds=2", Limits{WriteTimeout: 5 * time.Second}, 7 * time.Second},
		{"/debug/pprof/heap", Limits{WriteTimeout: 5 * time.Second}, 5 * time.Second},
		{"/debug/pprof/heap", Limits{WriteTimeout: -1}, 0},
	}
	for _, tt := range tests {
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		start := time.Now()
		Guard(tt.limits)(okHandler).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.target, nil))

		if tt.want == 0 {
			if !w.deadline.IsZero() {
				t.Errorf("%s: deadline set, want none", tt.target)
			}
			continue
		}
		got := w.deadline.Sub(start)
		if got < tt.want-time.Second || got > tt.want+time.Second {
			t.Errorf("%s: deadline in %v, want about %v", tt.target, got, tt.want)
		}
	}
}
