package parser_test

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/kellenff/yactt/internal/parser"
)

// Bench fixtures inline — kept larger than the *_Only fixtures in
// parser_test.go so the parse hot path is exercised, not micro-bench noise.
// ponytail: per-language inline rather than reading from tests/fixtures
// keeps the parser benchmark file zero-dependency on other packages.

const benchGoSrc = `package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Session struct {
	User      string
	ExpiresAt time.Time
}

type Repository interface {
	Save(ctx context.Context, s Session) error
	Load(ctx context.Context, name string) (Session, error)
}

var ErrExpired = errors.New("session expired")

func Login(repo Repository, user, pass string) (Session, error) {
	if user == "" || pass == "" {
		return Session{}, fmt.Errorf("missing credentials: %w", ErrExpired)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := Session{User: user, ExpiresAt: time.Now().Add(time.Hour)}
	if err := repo.Save(ctx, s); err != nil {
		return Session{}, err
	}
	return repo.Load(ctx, user)
}

func (s *Session) Valid(now time.Time) bool {
	return s.ExpiresAt.After(now)
}

func (s *Session) Extend(d time.Duration) {
	s.ExpiresAt = s.ExpiresAt.Add(d)
}
`

const benchPySrc = `import time
from typing import Optional


class Session:
    def __init__(self, user: str, ttl: int = 3600) -> None:
        self.user = user
        self.expires_at = time.time() + ttl

    def valid(self, now: Optional[float] = None) -> bool:
        now = now or time.time()
        return self.expires_at > now

    def extend(self, delta: int) -> None:
        self.expires_at += delta


def login(user: str, password: str) -> Session:
    if not user or not password:
        raise ValueError("missing credentials")
    return Session(user=user)


def refresh(s: Session) -> Session:
    s.extend(3600)
    return s
`

const benchTSSrc = `interface Session {
  user: string;
  expiresAt: number;
}

class SessionImpl implements Session {
  user: string;
  expiresAt: number;
  constructor(user: string, ttl: number = 3600) {
    this.user = user;
    this.expiresAt = Date.now() + ttl * 1000;
  }
  valid(now: number = Date.now()): boolean {
    return this.expiresAt > now;
  }
  extend(delta: number): void {
    this.expiresAt += delta * 1000;
  }
}

function login(user: string, password: string): SessionImpl {
  if (!user || !password) throw new Error("missing credentials");
  return new SessionImpl(user);
}

function refresh(s: Session): Session {
  (s as SessionImpl).extend(3600);
  return s;
}
`

// benchParse exercises the full parse + symbol-extraction path used per
// source file during repo.Load. Wraps the Grammar() so the Language
// dispatch cost is included.
func benchParse(b *testing.B, lang parser.Language, src string) {
	b.Helper()
	srcBytes := []byte(src)
	g := lang.Grammar()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root, err := sitter.ParseCtx(ctx, srcBytes, g)
		if err != nil {
			b.Fatalf("ParseCtx: %v", err)
		}
		if _, err := parser.ExtractSymbols(lang, root, srcBytes); err != nil {
			b.Fatalf("ExtractSymbols: %v", err)
		}
	}
}

func BenchmarkParseGolang(b *testing.B)     { benchParse(b, parser.Go{}, benchGoSrc) }
func BenchmarkParsePython(b *testing.B)     { benchParse(b, parser.Python{}, benchPySrc) }
func BenchmarkParseTypeScript(b *testing.B) { benchParse(b, parser.TypeScript{}, benchTSSrc) }

// BenchmarkDetectLanguage measures the extension→Language hot path used
// during the filepath.WalkDir sweep. ponytail: small slice, micro-bench
// shape; do not amplify with thousands of paths — the real cost is the
// map lookup, not iteration.
//
// Paths are restricted to known extensions — Detect returns
// ErrUnsupported for things like README.md, and the bench should
// exercise the happy path (the lossy path is exercised by store-level
// acceptance tests).
func BenchmarkDetectLanguage(b *testing.B) {
	paths := []string{
		"auth/login.go",
		"payments/pay.py",
		"ui/button.tsx",
		"ui/button.ts",
		"ui/app.jsx",
		"ui/app.js",
		"main.go",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, p := range paths {
			// Detect returns ErrUnsupported for unrecognised extensions
			// (e.g. .md, .json); those are filtered by the walker, not
			// by Detect itself. Don't treat that as a bench failure.
			_, _ = parser.Detect(p)
		}
	}
}
