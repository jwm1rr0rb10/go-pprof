package pprof

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGuardMinInterval(t *testing.T) {
	h := Guard(Limits{MinInterval: 3 * time.Second})(okHandler)

	if got := serve(h, http.MethodGet, "/debug/pprof/heap").Code; got != http.StatusOK {
		t.Fatalf("first heap: status %d, want 200", got)
	}
	rr := serve(h, http.MethodGet, "/debug/pprof/heap?debug=1")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second heap: status %d, want 429", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After = %q, want 3", got)
	}

	// Each endpoint has its own clock; cheap endpoints are never limited.
	for _, target := range []string{
		"/debug/pprof/goroutine",
		"/debug/pprof/allocs",
		"/debug/pprof/", "/debug/pprof/",
		"/debug/pprof/cmdline", "/debug/pprof/cmdline",
		"/debug/pprof/symbol", "/debug/pprof/symbol",
	} {
		if got := serve(h, http.MethodGet, target).Code; got != http.StatusOK {
			t.Errorf("%s: status %d, want 200", target, got)
		}
	}
}

func TestGuardMinIntervalExpires(t *testing.T) {
	h := Guard(Limits{MinInterval: 50 * time.Millisecond})(okHandler)
	serve(h, http.MethodGet, "/debug/pprof/goroutine")
	if got := serve(h, http.MethodGet, "/debug/pprof/goroutine").Code; got != http.StatusTooManyRequests {
		t.Fatalf("immediate retry: status %d, want 429", got)
	}
	time.Sleep(60 * time.Millisecond)
	if got := serve(h, http.MethodGet, "/debug/pprof/goroutine").Code; got != http.StatusOK {
		t.Fatalf("after interval: status %d, want 200", got)
	}
}

func TestGuardMinIntervalIgnoresUnknownProfiles(t *testing.T) {
	g := newRateLimiter(time.Hour)
	for i := 0; i < 100; i++ {
		if _, ok := g.allow("nope"+strconv.Itoa(i), time.Now()); !ok {
			t.Fatal("unknown profile was limited")
		}
	}
	if len(g.next) != 0 {
		t.Fatalf("rate limiter tracks %d unknown names, want 0", len(g.next))
	}
}

// A request rejected because the guard is busy must not use up the
// interval, otherwise a retry after the busy period would be rejected too.
func TestGuardBusyDoesNotConsumeInterval(t *testing.T) {
	inner, entered, release := blockingHandler()
	h := Guard(Limits{MaxConcurrent: 1, MinInterval: time.Hour})(inner)

	done := make(chan struct{})
	go func() { defer close(done); serve(h, http.MethodGet, "/debug/pprof/goroutine") }()
	<-entered

	rr := serve(h, http.MethodGet, "/debug/pprof/heap")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("heap while busy: status %d, want 429", rr.Code)
	}
	release()
	<-done

	go serve(h, http.MethodGet, "/debug/pprof/heap")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("heap after busy period was rate limited")
	}
}

func TestGuardBusyProfileRetryAfter(t *testing.T) {
	inner, entered, release := blockingHandler()
	defer release()
	h := Guard(Limits{MinInterval: -1})(inner)

	go serve(h, http.MethodGet, "/debug/pprof/profile?seconds=20")
	<-entered

	rr := serve(h, http.MethodGet, "/debug/pprof/profile")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rr.Code)
	}
	sec, err := strconv.Atoi(rr.Header().Get("Retry-After"))
	if err != nil || sec < 19 || sec > 20 {
		t.Errorf("Retry-After = %q, want the remaining ~20s of the running profile", rr.Header().Get("Retry-After"))
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:            "1",
		0:                       "1",
		time.Millisecond:        "1",
		time.Second:             "1",
		1001 * time.Millisecond: "2",
		20 * time.Second:        "20",
	} {
		if got := retryAfterSeconds(d); got != want {
			t.Errorf("retryAfterSeconds(%v) = %q, want %q", d, got, want)
		}
	}
}

// symbolServer serves the real pprof handlers behind a Guard.
func symbolServer(t *testing.T, l Limits) string {
	t.Helper()
	srv := httptest.NewServer(Handler(Guard(l)))
	t.Cleanup(srv.Close)
	return srv.URL + symbolPath
}

func TestGuardSymbolBody(t *testing.T) {
	url := symbolServer(t, Limits{MaxSymbolBodyBytes: 64})
	pc, _, _, _ := runtime.Caller(0)

	resp, err := http.Post(url, "text/plain", strings.NewReader(fmt.Sprintf("%#x", pc)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "TestGuardSymbolBody") {
		t.Fatalf("small body: status %d, body %q", resp.StatusCode, body)
	}

	big := strings.Repeat("0x1+", 100)
	resp, err = http.Post(url, "text/plain", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("large body with Content-Length: status %d, want 413", resp.StatusCode)
	}

	// Without Content-Length (chunked) the limit is enforced while reading.
	resp, err = http.Post(url, "text/plain", io.MultiReader(strings.NewReader(big)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("large chunked body: status %d, want 413", resp.StatusCode)
	}
}

// A client that sends the body of POST /symbol slowly must not hold a slot.
func TestGuardSymbolSlowBody(t *testing.T) {
	url := symbolServer(t, Limits{ReadTimeout: 200 * time.Millisecond})
	addr := strings.TrimPrefix(url, "http://")
	addr = addr[:strings.IndexByte(addr, '/')]

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	start := time.Now()
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n0x1", symbolPath)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status %d, want 408", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("slow body held the request for %v", elapsed)
	}
}

// Many clients hammer the guard at once. Every response must be 200 or 429,
// and afterwards every slot must be free again.
func TestGuardStress(t *testing.T) {
	var hold sync.WaitGroup
	entered := make(chan struct{}, 16)
	unblock := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("block") == "1" {
			entered <- struct{}{}
			<-unblock
		} else {
			time.Sleep(100 * time.Microsecond)
		}
		w.WriteHeader(http.StatusOK)
	})
	h := Guard(Limits{MinInterval: 5 * time.Millisecond})(inner)

	targets := []string{
		"/debug/pprof/profile?seconds=1",
		"/debug/pprof/trace?seconds=1",
		"/debug/pprof/heap",
		"/debug/pprof/goroutine?debug=1",
		"/debug/pprof/heap?gc=1",
		"/debug/pprof/",
	}
	var mu sync.Mutex
	statuses := map[int]int{}
	var wg sync.WaitGroup
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				code := serve(h, http.MethodGet, targets[(g+i)%len(targets)]).Code
				mu.Lock()
				statuses[code]++
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()

	for code, n := range statuses {
		switch code {
		case http.StatusOK, http.StatusTooManyRequests, http.StatusForbidden:
		default:
			t.Errorf("unexpected status %d (%d times)", code, n)
		}
	}
	if statuses[http.StatusTooManyRequests] == 0 {
		t.Error("no request was rejected with 429; the limits were not exercised")
	}

	// All DefaultMaxConcurrent slots, including the exclusive profile and
	// trace ones, must be free after the storm.
	time.Sleep(10 * time.Millisecond) // let the interval of every endpoint pass
	for _, target := range []string{
		"/debug/pprof/profile?block=1",
		"/debug/pprof/trace?block=1",
		"/debug/pprof/heap?block=1",
		"/debug/pprof/goroutine?block=1",
	} {
		hold.Add(1)
		go func(target string) { defer hold.Done(); serve(h, http.MethodGet, target) }(target)
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not get a slot: slots leaked", target)
		}
	}
	close(unblock)
	hold.Wait()
}

func BenchmarkGuardPassThrough(b *testing.B) {
	h := Guard(Limits{})(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func BenchmarkGuardHeap(b *testing.B) {
	h := Guard(Limits{MinInterval: -1})(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/heap?debug=1", nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func BenchmarkGuardClampedProfile(b *testing.B) {
	h := Guard(Limits{MinInterval: -1})(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/profile?seconds=3600", nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// Parallel requests with the default limits: most of them are rejected
// with 429, which is the path a misbehaving script hits.
func BenchmarkGuardParallelRejected(b *testing.B) {
	h := Guard(Limits{})(okHandler)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutine", nil)
		for pb.Next() {
			h.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
}
