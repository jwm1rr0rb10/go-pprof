package pprof

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/netip"
)

// Middleware wraps an http.Handler, e.g. to add authentication.
type Middleware func(http.Handler) http.Handler

// chain applies middlewares so that mws[0] is the outermost wrapper.
func chain(h http.Handler, mws []Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] != nil {
			h = mws[i](h)
		}
	}
	return h
}

// BasicAuth returns a Middleware that requires HTTP Basic authentication.
// Credentials are compared in constant time.
//
// It panics if username or password is empty, because that would make the
// check trivially bypassable. Basic auth sends credentials in clear text,
// so use it only over TLS or on a trusted network.
func BasicAuth(username, password string) Middleware {
	if username == "" || password == "" {
		panic("pprof: BasicAuth requires non-empty username and password")
	}
	wantUser := sha256.Sum256([]byte(username))
	wantPass := sha256.Sum256([]byte(password))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			gotUser := sha256.Sum256([]byte(user))
			gotPass := sha256.Sum256([]byte(pass))

			// Evaluate both comparisons to avoid leaking which one failed.
			userOK := subtle.ConstantTimeCompare(gotUser[:], wantUser[:])
			passOK := subtle.ConstantTimeCompare(gotPass[:], wantPass[:])

			if !ok || userOK&passOK != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="pprof", charset="UTF-8"`)
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AllowNetworks returns a Middleware that only allows requests whose
// remote address belongs to one of the given networks. Each entry is either
// a CIDR prefix ("10.0.0.0/8", "fd00::/8") or a single IP ("127.0.0.1").
// Other clients get 403 Forbidden.
//
// The check uses r.RemoteAddr only; X-Forwarded-For and similar headers are
// ignored on purpose because clients can forge them. Behind a reverse proxy
// the proxy's address is what gets checked.
func AllowNetworks(networks ...string) (Middleware, error) {
	if len(networks) == 0 {
		return nil, fmt.Errorf("pprof: AllowNetworks requires at least one network")
	}

	prefixes := make([]netip.Prefix, 0, len(networks))
	for _, n := range networks {
		p, err := parsePrefix(n)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, p)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !remoteAllowed(r.RemoteAddr, prefixes) {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("pprof: invalid network %q: must be a CIDR or an IP address", s)
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func remoteAllowed(remoteAddr string, prefixes []netip.Prefix) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
