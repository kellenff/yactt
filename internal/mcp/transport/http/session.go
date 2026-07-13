package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrSessionCap is returned by SessionManager.Allocate when the
// hard cap on concurrent sessions has been reached. The transport
// translates this into HTTP 503 with a structured JSON-RPC error.
var ErrSessionCap = errors.New("session cap reached")

// Session is the server-side state for one MCP client connection.
// It carries the Mcp-Session-Id, the remote address, and a child
// context that's cancelled on reap / explicit DELETE / daemon
// shutdown.
type Session struct {
	ID         string
	ClientAddr string
	CreatedAt  time.Time
	LastSeen   time.Time
	ctx        context.Context
	cancel     context.CancelFunc
}

// Ctx returns the session's child context. It is cancelled on
// reap, on explicit DELETE, or on daemon shutdown. HTTP handlers
// use this for SSE-stream lifecycle (the stream blocks on
// <-ctx.Done()).
func (s *Session) Ctx() context.Context { return s.ctx }

// SessionManager owns the session map, the parent context that
// cancels every session on shutdown, and the idle-reap helper.
// It's safe for concurrent use.
type SessionManager struct {
	idleTimeout time.Duration
	maxSessions int

	parentCtx    context.Context
	parentCancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*Session

	stopped atomic.Bool
}

// NewSessionManager constructs a manager. idleTimeout is how long
// a session can go without a request before reap; maxSessions is
// the hard cap on concurrent allocations. Both must be > 0; the
// caller (Server.NewServer) is responsible for sane defaults.
func NewSessionManager(idleTimeout time.Duration, maxSessions int) *SessionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &SessionManager{
		idleTimeout:  idleTimeout,
		maxSessions:  maxSessions,
		parentCtx:    ctx,
		parentCancel: cancel,
		sessions:     make(map[string]*Session),
	}
}

// Allocate creates a new session and registers it. Returns
// ErrSessionCap if the cap is reached, or an error if the manager
// has been Stop'd.
func (sm *SessionManager) Allocate(clientAddr string) (*Session, error) {
	if sm.stopped.Load() {
		return nil, errors.New("session manager stopped")
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.sessions) >= sm.maxSessions {
		return nil, ErrSessionCap
	}
	id := newSessionID()
	ctx, cancel := context.WithCancel(sm.parentCtx)
	now := time.Now()
	s := &Session{
		ID:         id,
		ClientAddr: clientAddr,
		CreatedAt:  now,
		LastSeen:   now,
		ctx:        ctx,
		cancel:     cancel,
	}
	sm.sessions[id] = s
	return s, nil
}

// Lookup returns the session for `id`, or ok=false. Does not
// touch LastSeen — callers that care about idle state should
// call Touch separately.
func (sm *SessionManager) Lookup(id string) (*Session, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.sessions[id]
	return s, ok
}

// Touch updates the session's LastSeen timestamp. Idempotent on
// unknown ids (no error, no-op).
func (sm *SessionManager) Touch(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s, ok := sm.sessions[id]; ok {
		s.LastSeen = time.Now()
	}
}

// Delete removes and cancels a session. Idempotent on missing ids.
func (sm *SessionManager) Delete(id string) {
	sm.mu.Lock()
	s, ok := sm.sessions[id]
	if ok {
		delete(sm.sessions, id)
	}
	sm.mu.Unlock()
	if ok {
		s.cancel()
	}
}

// Count returns the current session count.
func (sm *SessionManager) Count() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return len(sm.sessions)
}

// Reap removes all sessions whose LastSeen is older than
// idleTimeout. Returns the number removed. Called by the
// reaper goroutine on a 30s tick.
func (sm *SessionManager) Reap() int {
	cutoff := time.Now().Add(-sm.idleTimeout)
	var toDelete []string
	sm.mu.Lock()
	for id, s := range sm.sessions {
		if s.LastSeen.Before(cutoff) {
			toDelete = append(toDelete, id)
		}
	}
	sm.mu.Unlock()
	for _, id := range toDelete {
		sm.Delete(id)
	}
	return len(toDelete)
}

// Stop cancels every session and prevents further allocations.
// Idempotent.
func (sm *SessionManager) Stop() {
	if !sm.stopped.CompareAndSwap(false, true) {
		return
	}
	sm.parentCancel()
	sm.mu.Lock()
	sessions := sm.sessions
	sm.sessions = make(map[string]*Session)
	sm.mu.Unlock()
	for _, s := range sessions {
		s.cancel()
	}
}

// All returns a snapshot of all current sessions. Caller must
// not mutate. Used by graceful shutdown to send SSE shutdown
// events to every open stream.
func (sm *SessionManager) All() []*Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	out := make([]*Session, 0, len(sm.sessions))
	for _, s := range sm.sessions {
		out = append(out, s)
	}
	return out
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}