package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthChecker_NoToken_NoCheck(t *testing.T) {
	ac := NewAuthChecker("", "127.0.0.1")
	r := httptest.NewRequest("POST", "/x", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if err := ac.Check(r); err != nil {
		t.Fatalf("Check on loopback without token should pass, got %v", err)
	}
}

func TestAuthChecker_TokenSet_Loopback_NoHeader(t *testing.T) {
	ac := NewAuthChecker("secret", "127.0.0.1")
	r := httptest.NewRequest("POST", "/x", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if err := ac.Check(r); err != ErrUnauthorized {
		t.Fatalf("Check should require auth when token set, got %v", err)
	}
}

func TestAuthChecker_TokenSet_Loopback_RightToken(t *testing.T) {
	ac := NewAuthChecker("secret", "127.0.0.1")
	r := httptest.NewRequest("POST", "/x", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer secret")
	if err := ac.Check(r); err != nil {
		t.Fatalf("Check on correct token should pass, got %v", err)
	}
}

func TestAuthChecker_NonLoopback_NoToken(t *testing.T) {
	ac := NewAuthChecker("", "0.0.0.0")
	r := httptest.NewRequest("POST", "/x", nil)
	r.RemoteAddr = "192.168.1.5:1234"
	if err := ac.Check(r); err != ErrAuthRequired {
		t.Fatalf("non-loopback without token should error with ErrAuthRequired, got %v", err)
	}
}

func TestAuthChecker_NonLoopback_WithToken(t *testing.T) {
	ac := NewAuthChecker("secret", "0.0.0.0")
	r := httptest.NewRequest("POST", "/x", nil)
	r.RemoteAddr = "192.168.1.5:1234"
	r.Header.Set("Authorization", "Bearer secret")
	if err := ac.Check(r); err != nil {
		t.Fatalf("Check with token on non-loopback should pass, got %v", err)
	}
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:1234", true},
		{"127.0.0.5:9999", true},
		{"[::1]:8080", true},
		{"192.168.1.5:1234", false},
		{"10.0.0.1:80", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.addr); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.5", true},
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false},
		{"192.168.1.5", false},
		{"example.com", false},
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.host); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

var _ = http.StatusOK // keep import alive