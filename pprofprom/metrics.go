// Package pprofprom exports Prometheus metrics for pprof requests guarded
// by [pprof.Guard] or served by [pprof.Server].
//
// It is a separate module so that the pprof package itself keeps no
// dependencies outside the standard library.
//
//	m, err := pprofprom.New(prometheus.DefaultRegisterer)
//	if err != nil {
//		log.Fatal(err)
//	}
//	srv := pprof.NewServer(pprof.Config{
//		Limits: pprof.Limits{OnRequest: m.Observe},
//	})
//
// Metrics (all labels have a bounded set of values):
//
//	pprof_requests_total{endpoint, code, rejected}   counter
//	pprof_request_duration_seconds{endpoint}         histogram
//	pprof_profiling_seconds_total{endpoint}          counter
//
// endpoint is a standard pprof endpoint name ("profile", "heap", ...) or
// "other"; rejected is "" for served requests or a short reason such as
// "busy" or "rate_limited". pprof_profiling_seconds_total sums the
// effective "seconds" of served /profile, /trace and delta profile
// requests, i.e. how long the service has been profiled.
//
// To add a prefix or constant labels, wrap the registerer with
// prometheus.WrapRegistererWithPrefix or prometheus.WrapRegistererWith.
package pprofprom

import (
	"strconv"

	"github.com/jwm1rr0rb10/go-pprof"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics collects pprof request metrics. Its Observe method is meant to
// be used as [pprof.Limits.OnRequest].
type Metrics struct {
	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	profiling *prometheus.CounterVec
}

// DurationBuckets are the histogram buckets of
// pprof_request_duration_seconds: from fast snapshots (/heap, /goroutine)
// to the longest CPU profile allowed by the default limits.
var DurationBuckets = []float64{0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 120}

// New creates the metrics and registers them with reg (the default
// registerer if reg is nil).
func New(reg prometheus.Registerer) (*Metrics, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pprof_requests_total",
			Help: "Requests to pprof endpoints, including rejected ones.",
		}, []string{"endpoint", "code", "rejected"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pprof_request_duration_seconds",
			Help:    "Time spent serving pprof requests.",
			Buckets: DurationBuckets,
		}, []string{"endpoint"}),
		profiling: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pprof_profiling_seconds_total",
			Help: "Requested profiling time of served /profile, /trace and delta profile requests.",
		}, []string{"endpoint"}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.duration, m.profiling} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// MustNew is like [New] but panics on error.
func MustNew(reg prometheus.Registerer) *Metrics {
	m, err := New(reg)
	if err != nil {
		panic(err)
	}
	return m
}

// Observe records one finished request. Use it as [pprof.Limits.OnRequest],
// or call it from your own OnRequest function next to audit logging.
func (m *Metrics) Observe(info pprof.RequestInfo) {
	endpoint := endpointLabel(info.Endpoint)
	m.requests.WithLabelValues(endpoint, strconv.Itoa(info.Status), rejectedLabel(info.Rejected)).Inc()
	m.duration.WithLabelValues(endpoint).Observe(info.Duration.Seconds())
	if info.Rejected == "" && info.Seconds > 0 {
		m.profiling.WithLabelValues(endpoint).Add(info.Seconds)
	}
}

var knownEndpoints = map[string]bool{
	"index": true, "cmdline": true, "profile": true, "symbol": true, "trace": true,
	"allocs": true, "block": true, "goroutine": true, "heap": true, "mutex": true, "threadcreate": true,
}

// endpointLabel keeps the label bounded: any path under /debug/pprof/ is
// an endpoint name, so arbitrary requests would create new series.
func endpointLabel(name string) string {
	if knownEndpoints[name] {
		return name
	}
	return "other"
}

var rejectedLabels = map[string]string{
	"":                          "",
	pprof.RejectedDisabled:      "disabled",
	pprof.RejectedMethod:        "method",
	pprof.RejectedForcedGC:      "forced_gc",
	pprof.RejectedGoroutineDump: "goroutine_dump",
	pprof.RejectedBusy:          "busy",
	pprof.RejectedRateLimited:   "rate_limited",
	pprof.RejectedBodyTooLarge:  "body_too_large",
	pprof.RejectedReadTimeout:   "read_timeout",
	pprof.RejectedBadBody:       "bad_body",
}

func rejectedLabel(reason string) string {
	if l, ok := rejectedLabels[reason]; ok {
		return l
	}
	return "other"
}
