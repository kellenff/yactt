package http

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
)

var (
	// ErrUnauthorized is returned when a token is configured but
	// the request fails the bearer check (missing header, wrong
	// value). The middleware translates this to HTTP 401.
	ErrUnauthorized = errors.New("unauthorized")

	// ErrAuthRequired is returned when no token is configured
	// but the bind address is non-loopback — a daemon on a
	// public bind with no auth is a footgun we refuse to serve.
	// The middleware translates this to HTTP 403.
	ErrAuthRequired = errors.New("auth required on non-loopback bind")
)

// AuthChecker gates requests by token (when configured) and by
// loopback address (always). Loopback requests bypass token
// checks when no token is set; non-loopback requests require a
// token and refuse to serve without one.
type AuthChecker struct {
	token       string // empty = no token required
	bindIsPublic bool   // true when bind address is non-loopback
}

// NewAuthChecker constructs a checker. `bindAddr` is the
// configured listen address (e.g. "127.0.0.1" or "0.0.0.0").
func NewAuthChecker(token, bindAddr string) *AuthChecker {
	return &AuthChecker{
		token:       token,
		bindIsPublic: !isLoopbackHost(bindAddr),
	}
}

// Check returns nil when the request is allowed; an error
// otherwise. The middleware translates the error into the
// right HTTP status (401 for unauthorized, 403 for "auth
// required on public bind").
func (a *AuthChecker) Check(r *http.Request) error {
	if a.token == "" && !a.bindIsPublic {
		return nil // loopback, no token — open
	}
	if a.token == "" && a.bindIsPublic {
		// Public bind, no token configured — refuse to serve.
		return ErrAuthRequired
	}
	// Token configured — must present it.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ErrUnauthorized
	}
	got := strings.TrimPrefix(auth, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
		return ErrUnauthorized
	}
	return nil
}

// isLoopback reports whether `addr` is a loopback address.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return isLoopbackHost(host)
}

// isLoopbackHost reports whether `host` is a loopback host.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}