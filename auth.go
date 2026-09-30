package pprof

import (
	"context"
	"errors"
	"net/http"
)

// ErrUnauthenticated is returned by an [AuthorizeFunc] when the request
// carries no usable identity. [Authorize] answers it with 401; any other
// error means the identity is known but not allowed, and gets 403.
var ErrUnauthenticated = errors.New("pprof: unauthenticated")

// AuthorizeFunc identifies the caller of a pprof request and decides
// whether it may proceed. It returns the caller's identity (a user name, a
// service name, a SPIFFE ID, ...) or an error to deny the request.
type AuthorizeFunc func(r *http.Request) (principal string, err error)

type principalKey struct{}

// WithPrincipal returns a copy of ctx carrying principal. [Authorize] and
// [BasicAuth] call it; custom authentication middleware can call it too,
// so that [RequestInfo.Principal] is filled in audit logs.
func WithPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFromContext returns the principal stored by [WithPrincipal], or
// "" if there is none.
func PrincipalFromContext(ctx context.Context) string {
	p, _ := ctx.Value(principalKey{}).(string)
	return p
}

// Authorize returns a Middleware that calls fn for every request. Denied
// requests get 401 (fn returned [ErrUnauthenticated]) or 403 (any other
// error) with a generic body: the error text is never sent to the client,
// so log it inside fn if needed. Allowed requests carry the principal in
// their context, see [PrincipalFromContext] and [RequestInfo.Principal].
//
// It panics if fn is nil.
func Authorize(fn AuthorizeFunc) Middleware {
	if fn == nil {
		panic("pprof: Authorize requires a non-nil function")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := fn(r)
			switch {
			case errors.Is(err, ErrUnauthenticated):
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			case err != nil:
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// ClientCertPrincipal returns the identity of a client authenticated with
// mTLS: the first SPIFFE ID (a "spiffe://" URI SAN) of the verified leaf
// certificate, or its Subject Common Name if it has none. It returns
// [ErrUnauthenticated] if the connection has no verified client
// certificate, so it is meant to be used inside an [AuthorizeFunc]:
//
//	pprof.Authorize(func(r *http.Request) (string, error) {
//		id, err := pprof.ClientCertPrincipal(r)
//		if err != nil {
//			return "", err
//		}
//		if !allowed[id] {
//			return id, errors.New("not in the pprof allowlist")
//		}
//		return id, nil
//	})
//
// Only certificates verified by the server count, so the server's
// tls.Config must set ClientAuth to tls.RequireAndVerifyClientCert or
// tls.VerifyClientCertIfGiven; see [Config.TLSConfig].
func ClientCertPrincipal(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		return "", ErrUnauthenticated
	}
	leaf := r.TLS.VerifiedChains[0][0]
	for _, u := range leaf.URIs {
		if u.Scheme == "spiffe" {
			return u.String(), nil
		}
	}
	if leaf.Subject.CommonName != "" {
		return leaf.Subject.CommonName, nil
	}
	return "", ErrUnauthenticated
}
