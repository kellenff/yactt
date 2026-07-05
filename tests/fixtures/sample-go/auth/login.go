// Package auth handles user authentication for the sample service.
//
// The Login function is the public entry point; SetSession persists the
// authenticated identity to the session store.
package auth

import (
	"fmt"

	"github.com/kellenff/yactt/tests/fixtures/sample-go/payments"
)

// ErrInvalidToken is returned when a token cannot be validated.
var ErrInvalidToken = fmt.Errorf("invalid token")

// Login authenticates a user by token. It returns a session token on success
// or ErrInvalidToken if the token is rejected.
//
// The function traces out the typical happy path: validation, then session
// creation, then a payment charge. Errors are returned verbatim — they keep
// the call site readable. The payments.Charge call is the cross-package
// edge the Tier-1 LSP subgraph resolves; in tree-sitter-only mode it still
// appears as a syntactic callee.
func Login(token string) (string, error) {
	if err := ValidateToken(token); err != nil {
		return "", err
	}
	sess, err := SetSession(token)
	if err != nil {
		return "", err
	}
	if err := payments.Charge(sess); err != nil {
		return "", err
	}
	return sess, nil
}

// ValidateToken inspects the token and returns an error if it is malformed.
//
// MVP implementation: rejects empty tokens. Future: cryptographic checks.
func ValidateToken(token string) error {
	if token == "" {
		return ErrInvalidToken
	}
	return nil
}

// SetSession opens a session for the validated token.
//
// This is the persistence boundary — it calls into the session store.
func SetSession(token string) (string, error) {
	// Simple deterministic ID for the fixture.
	return "sess-" + token, nil
}
