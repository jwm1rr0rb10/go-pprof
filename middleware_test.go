package pprof

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestBasicAuth(t *testing.T) {
	h := BasicAuth("admin", "secret")(okHandler)

	tests := []struct {
		name       string
		user, pass string
		setAuth    bool
		want       int
	}{
		{"no credentials", "", "", false, http.StatusUnauthorized},
		{"wrong password", "admin", "wrong", true, http.StatusUnauthorized},
		{"wrong user", "root", "secret", true, http.StatusUnauthorized},
		{"empty credentials", "", "", true, http.StatusUnauthorized},
		{"valid", "admin", "secret", true, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
			if tt.setAuth {
				req.SetBasicAuth(tt.user, tt.pass)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != tt.want {
				t.Errorf("status %d, want %d", rr.Code, tt.want)
			}
			if tt.want == http.StatusUnauthorized && rr.Header().Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate header")
			}
		})
	}
}

func TestBasicAuthPanicsOnEmptyCredentials(t *testing.T) {
	for _, c := range [][2]string{{"", "p"}, {"u", ""}, {"", ""}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("BasicAuth(%q, %q) did not panic", c[0], c[1])
				}
			}()
			BasicAuth(c[0], c[1])
		}()
	}
}

func TestAllowNetworks(t *testing.T) {
	mw, err := AllowNetworks("192.0.2.0/24", "10.1.2.3", "fd00::/8")
	if err != nil {
		t.Fatalf("AllowNetworks: %v", err)
	}
	h := mw(okHandler)

	tests := []struct {
		remote string
		want   int
	}{
		{"192.0.2.55:1234", http.StatusOK},
		{"10.1.2.3:1234", http.StatusOK},
		{"10.1.2.4:1234", http.StatusForbidden},
		{"[::ffff:192.0.2.7]:1234", http.StatusOK}, // IPv4-mapped IPv6
		{"[fd12::1]:1234", http.StatusOK},
		{"[fe80::1%eth0]:1234", http.StatusForbidden},
		{"203.0.113.1:1234", http.StatusForbidden},
		{"garbage", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
			req.RemoteAddr = tt.remote
			req.Header.Set("X-Forwarded-For", "192.0.2.1") // must be ignored
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Errorf("status %d, want %d", rr.Code, tt.want)
			}
		})
	}
}

func TestAllowNetworksErrors(t *testing.T) {
	if _, err := AllowNetworks(); err == nil {
		t.Error("expected error for empty list")
	}
	if _, err := AllowNetworks("10.0.0.0/8", "not-an-ip"); err == nil {
		t.Error("expected error for invalid network")
	}
	if _, err := AllowNetworks("10.0.0.0/33"); err == nil {
		t.Error("expected error for invalid prefix length")
	}
}
