package auth

import "testing"

// TestLogin_Success covers the happy path: a non-empty token returns a session.
func TestLogin_Success(t *testing.T) {
	got, err := Login("abc")
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty session")
	}
}

// TestLogin_Empty exercises the empty-token rejection.
func TestLogin_Empty(t *testing.T) {
	_, err := Login("")
	if err == nil {
		t.Fatal("expected error on empty token")
	}
}
