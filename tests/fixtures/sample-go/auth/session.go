package auth

import "time"

// Session is the typed record stored for an authenticated user.
//
// The session store is a tiny in-memory map for the fixture. Real deployments
// would persist it to a database.
type Session struct {
	ID        string
	CreatedAt time.Time
}

// NewSession constructs a Session record from a token-derived ID.
func NewSession(id string) Session {
	return Session{ID: id, CreatedAt: time.Now()}
}

// User is the application's view of an authenticated user.
type User struct {
	Name  string
	Email string
}

// Describe returns a human-readable summary of u.
func (u User) Describe() string {
	return u.Name + " <" + u.Email + ">"
}
