package pprofprom

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jwm1rr0rb10/go-pprof"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserve(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	m := MustNew(reg)

	m.Observe(pprof.RequestInfo{Endpoint: "profile", Status: 200, Duration: 2 * time.Second, Seconds: 2})
	m.Observe(pprof.RequestInfo{Endpoint: "profile", Status: 429, Rejected: pprof.RejectedBusy, Seconds: 30})
	m.Observe(pprof.RequestInfo{Endpoint: "heap", Status: 403, Rejected: pprof.RejectedForcedGC})
	m.Observe(pprof.RequestInfo{Endpoint: "random-" + strings.Repeat("x", 10), Status: 404})
	m.Observe(pprof.RequestInfo{Endpoint: "heap", Status: 500, Rejected: "something new"})

	want := `
# HELP pprof_requests_total Requests to pprof endpoints, including rejected ones.
# TYPE pprof_requests_total counter
pprof_requests_total{code="200",endpoint="profile",rejected=""} 1
pprof_requests_total{code="403",endpoint="heap",rejected="forced_gc"} 1
pprof_requests_total{code="404",endpoint="other",rejected=""} 1
pprof_requests_total{code="429",endpoint="profile",rejected="busy"} 1
pprof_requests_total{code="500",endpoint="heap",rejected="other"} 1
# HELP pprof_profiling_seconds_total Requested profiling time of served /profile, /trace and delta profile requests.
# TYPE pprof_profiling_seconds_total counter
pprof_profiling_seconds_total{endpoint="profile"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "pprof_requests_total", "pprof_profiling_seconds_total"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(m.duration); n != 3 {
		t.Errorf("duration histogram has %d series, want 3 (profile, heap, other)", n)
	}
}

func TestEveryRejectedReasonHasALabel(t *testing.T) {
	for _, reason := range []string{
		pprof.RejectedDisabled, pprof.RejectedMethod, pprof.RejectedForcedGC,
		pprof.RejectedGoroutineDump, pprof.RejectedBusy, pprof.RejectedRateLimited,
		pprof.RejectedBodyTooLarge, pprof.RejectedReadTimeout, pprof.RejectedBadBody,
	} {
		if got := rejectedLabel(reason); got == "other" || got == "" {
			t.Errorf("reason %q has no dedicated label", reason)
		}
	}
}

func TestNewRegisterError(t *testing.T) {
	reg := prometheus.NewRegistry()
	MustNew(reg)
	if _, err := New(reg); err == nil {
		t.Fatal("registering twice did not fail")
	}
}

// End to end with the real Guard.
func TestWithGuard(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := MustNew(reg)
	h := pprof.Handler(pprof.Guard(pprof.Limits{OnRequest: m.Observe}))

	for _, target := range []string{"/debug/pprof/heap", "/debug/pprof/heap", "/debug/pprof/heap?gc=1"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	}

	if got := testutil.ToFloat64(m.requests.WithLabelValues("heap", "200", "")); got != 1 {
		t.Errorf("served heap = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues("heap", "429", "rate_limited")); got != 1 {
		t.Errorf("rate limited heap = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues("heap", "403", "forced_gc")); got != 1 {
		t.Errorf("forced GC heap = %v, want 1", got)
	}
}

func BenchmarkObserve(b *testing.B) {
	m := MustNew(prometheus.NewRegistry())
	info := pprof.RequestInfo{Endpoint: "heap", Status: 200, Duration: time.Millisecond}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.Observe(info)
	}
}
