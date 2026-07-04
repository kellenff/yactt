// Package auth handles user authentication for the sample service.
//
// The Login function is the public entry point; SetSession persists the
// authenticated identity to the session store.
package auth

import "fmt"

// ErrInvalidToken is returned when a token cannot be validated.
var ErrInvalidToken = fmt.Errorf("invalid token")

// Login authenticates a user by token. It returns a session token on success
// or ErrInvalidToken if the token is rejected.
//
// The function traces out the typical happy path: validation then session
// creation. Errors are returned verbatim — they keep the call site readable.
func Login(token string) (string, error) {
	if err := ValidateToken(token); err != nil {
		return "", err
	}
	sess, err := SetSession(token)
	if err != nil {
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
