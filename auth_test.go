package pprof

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// principalHandler writes the principal from the request context.
var principalHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(PrincipalFromContext(r.Context())))
})

func TestAuthorize(t *testing.T) {
	h := Authorize(func(r *http.Request) (string, error) {
		switch r.Header.Get("X-User") {
		case "":
			return "", ErrUnauthenticated
		case "alice":
			return "alice", nil
		default:
			return "", errors.New("secret reason")
		}
	})(principalHandler)

	tests := []struct {
		user     string
		want     int
		wantBody string
	}{
		{"", http.StatusUnauthorized, "Unauthorized\n"},
		{"mallory", http.StatusForbidden, "Forbidden\n"}, // the error text is not leaked
		{"alice", http.StatusOK, "alice"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
		req.Header.Set("X-User", tt.user)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != tt.want || rr.Body.String() != tt.wantBody {
			t.Errorf("user %q: status %d body %q, want %d %q", tt.user, rr.Code, rr.Body.String(), tt.want, tt.wantBody)
		}
	}
}

func TestAuthorizeWrappedUnauthenticated(t *testing.T) {
	h := Authorize(func(*http.Request) (string, error) {
		return "", errors.Join(ErrUnauthenticated, errors.New("token expired"))
	})(okHandler)
	if got := serve(h, http.MethodGet, "/debug/pprof/").Code; got != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", got)
	}
}

func TestAuthorizePanicsOnNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Authorize(nil) did not panic")
		}
	}()
	Authorize(nil)
}

func TestBasicAuthSetsPrincipal(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.SetBasicAuth("admin", "secret")
	rr := httptest.NewRecorder()
	BasicAuth("admin", "secret")(principalHandler).ServeHTTP(rr, req)
	if rr.Body.String() != "admin" {
		t.Errorf("principal = %q, want admin", rr.Body.String())
	}
}

func TestGuardReportsPrincipal(t *testing.T) {
	var got RequestInfo
	h := Handler(
		Authorize(func(*http.Request) (string, error) { return "svc-a", nil }),
		Guard(Limits{OnRequest: func(i RequestInfo) { got = i }}),
	)
	serve(h, http.MethodGet, "/debug/pprof/cmdline")
	if got.Principal != "svc-a" {
		t.Errorf("RequestInfo.Principal = %q, want svc-a", got.Principal)
	}
}

func TestClientCertPrincipal(t *testing.T) {
	spiffe, _ := url.Parse("spiffe://example.org/ns/prod/sa/profiler")
	other, _ := url.Parse("https://example.org/not-spiffe")

	tests := []struct {
		name  string
		state *tls.ConnectionState
		want  string
		err   error
	}{
		{"plain HTTP", nil, "", ErrUnauthenticated},
		{"no verified chain", &tls.ConnectionState{}, "", ErrUnauthenticated},
		{"SPIFFE ID wins", leafState(&x509.Certificate{URIs: []*url.URL{other, spiffe}, Subject: pkix.Name{CommonName: "cn"}}), spiffe.String(), nil},
		{"common name", leafState(&x509.Certificate{URIs: []*url.URL{other}, Subject: pkix.Name{CommonName: "profiler"}}), "profiler", nil},
		{"no identity", leafState(&x509.Certificate{}), "", ErrUnauthenticated},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
		req.TLS = tt.state
		got, err := ClientCertPrincipal(req)
		if got != tt.want || !errors.Is(err, tt.err) {
			t.Errorf("%s: got %q, %v; want %q, %v", tt.name, got, err, tt.want, tt.err)
		}
	}
}

func leafState(leaf *x509.Certificate) *tls.ConnectionState {
	return &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf}}}
}

// End to end: an mTLS server identifies the client by its SPIFFE ID and
// reports it to OnRequest; clients without a certificate cannot connect.
func TestServerMTLS(t *testing.T) {
	ca := newTestCA(t)
	serverCert := ca.issue(t, "127.0.0.1", nil, x509.ExtKeyUsageServerAuth)
	id, _ := url.Parse("spiffe://example.org/profiler")
	clientCert := ca.issue(t, "", id, x509.ExtKeyUsageClientAuth)

	principals := make(chan string, 1)
	s := NewServer(Config{
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientCAs:    ca.pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS12,
		},
		Middlewares: []Middleware{Authorize(ClientCertPrincipal)},
		Limits:      Limits{OnRequest: func(i RequestInfo) { principals <- i.Principal }},
	})
	ln := localListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx, ln) }()
	url := "https://" + ln.Addr().String() + "/debug/pprof/cmdline"

	client := func(certs ...tls.Certificate) *http.Client {
		return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: ca.pool, Certificates: certs, MinVersion: tls.VersionTLS12},
		}}
	}

	resp, err := client(clientCert).Get(url)
	if err != nil {
		t.Fatalf("mTLS request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if got := <-principals; got != id.String() {
		t.Errorf("principal = %q, want %q", got, id)
	}

	if resp, err := client().Get(url); err == nil {
		resp.Body.Close()
		t.Fatalf("request without a client certificate succeeded: status %d", resp.StatusCode)
	}
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// issue creates a leaf certificate for ip (server) or with the URI SAN id
// (client).
func (ca *testCA) issue(t *testing.T, ip string, id *url.URL, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	if ip != "" {
		tmpl.IPAddresses = []net.IP{net.ParseIP(ip)}
	}
	if id != nil {
		tmpl.URIs = []*url.URL{id}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
