package http

import (
	"testing"
	"time"
)

func TestSessionManager_Allocate(t *testing.T) {
	sm := NewSessionManager(5*time.Minute, 10)
	defer sm.Stop()

	s, err := sm.Allocate("127.0.0.1:1234")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == "" {
		t.Fatal("session ID should be populated")
	}
	if s.ClientAddr != "127.0.0.1:1234" {
		t.Fatalf("ClientAddr = %q, want 127.0.0.1:1234", s.ClientAddr)
	}
}

func TestSessionManager_Lookup(t *testing.T) {
	sm := NewSessionManager(5*time.Minute, 10)
	defer sm.Stop()
	s, _ := sm.Allocate("127.0.0.1:1234")

	got, ok := sm.Lookup(s.ID)
	if !ok || got.ID != s.ID {
		t.Fatalf("Lookup = %v, %v; want %v, true", got, ok, s)
	}
}

func TestSessionManager_LookupMissing(t *testing.T) {
	sm := NewSessionManager(5*time.Minute, 10)
	defer sm.Stop()
	_, ok := sm.Lookup("nonexistent")
	if ok {
		t.Fatalf("Lookup of missing should return ok=false")
	}
}

func TestSessionManager_Delete(t *testing.T) {
	sm := NewSessionManager(5*time.Minute, 10)
	defer sm.Stop()
	s, _ := sm.Allocate("127.0.0.1:1234")
	sm.Delete(s.ID)
	_, ok := sm.Lookup(s.ID)
	if ok {
		t.Fatalf("session should be gone after Delete")
	}
}

func TestSessionManager_Cap(t *testing.T) {
	sm := NewSessionManager(5*time.Minute, 2)
	defer sm.Stop()
	if _, err := sm.Allocate("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.Allocate("y"); err != nil {
		t.Fatal(err)
	}
	_, err := sm.Allocate("z")
	if err == nil {
		t.Fatalf("Allocate beyond cap should fail")
	}
	if err != ErrSessionCap {
		t.Fatalf("err = %v, want ErrSessionCap", err)
	}
}

func TestSessionManager_IdleReap(t *testing.T) {
	sm := NewSessionManager(50*time.Millisecond, 10)
	defer sm.Stop()
	s, _ := sm.Allocate("x")
	sm.Touch(s.ID)

	time.Sleep(100 * time.Millisecond)
	n := sm.Reap()
	if n < 1 {
		t.Fatalf("Reap should have removed at least 1 session, removed %d", n)
	}
	if _, ok := sm.Lookup(s.ID); ok {
		t.Fatalf("session should be reaped")
	}
}

func TestSessionManager_Count(t *testing.T) {
	sm := NewSessionManager(5*time.Minute, 10)
	defer sm.Stop()
	for i := 0; i < 3; i++ {
		if _, err := sm.Allocate("x"); err != nil {
			t.Fatal(err)
		}
	}
	if got := sm.Count(); got != 3 {
		t.Fatalf("Count = %d, want 3", got)
	}
}

func TestSessionManager_ShutdownCancels(t *testing.T) {
	sm := NewSessionManager(5*time.Minute, 10)
	s, _ := sm.Allocate("x")
	done := make(chan struct{})
	go func() {
		<-s.Ctx().Done()
		close(done)
	}()
	sm.Stop()
	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatalf("session ctx should be cancelled on Stop")
	}
}