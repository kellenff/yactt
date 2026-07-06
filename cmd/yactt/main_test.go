package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/audit"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// TestBuildStartupInfo exercises the audit-startup snapshot helper.
// Pins the on-wire shape so an unintentional field rename or a
// missing field breaks here, not at 3am in a downstream parser.
func TestBuildStartupInfo(t *testing.T) {
	fx := repofixture.New(t)
	repo, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = repo.Close() }()

	info := buildStartupInfo(repo, nil, "deadbeef")
	if info.Version == "" {
		t.Error("Version not populated")
	}
	if info.RepoRoot != fx.Root {
		t.Errorf("RepoRoot = %q, want %q", info.RepoRoot, fx.Root)
	}
	if info.MaxFiles != store.DefaultMaxFiles {
		t.Errorf("MaxFiles = %d, want %d", info.MaxFiles, store.DefaultMaxFiles)
	}
	if info.LoadedFiles <= 0 {
		t.Errorf("LoadedFiles = %d, want >0", info.LoadedFiles)
	}
	if info.BinarySHA256 != "deadbeef" {
		t.Errorf("BinarySHA256 = %q", info.BinarySHA256)
	}
	// Grammars must include go, javascript, typescript (the wired set).
	wantGrammars := map[string]bool{"go": false, "javascript": false, "typescript": false}
	for _, g := range info.Grammars {
		if _, ok := wantGrammars[g]; ok {
			wantGrammars[g] = true
		}
	}
	for k, seen := range wantGrammars {
		if !seen {
			t.Errorf("grammar %q missing from startup info: %v", k, info.Grammars)
		}
	}
	// LSP entries must be one per grammar; absent entries are
	// represented by an empty Tool/Version (still a row, so the
	// host sees the language is wired even when the server is
	// missing on PATH).
	if len(info.LSP) != len(info.Grammars) {
		t.Errorf("LSP rows = %d, want %d (one per grammar)", len(info.LSP), len(info.Grammars))
	}
}

// TestEmitStartupLine confirms the round-trip: buildStartupInfo +
// EmitStartup produces one valid JSON line on stderr.
func TestEmitStartupLine(t *testing.T) {
	fx := repofixture.New(t)
	repo, _, err := store.Load(fx.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = repo.Close() }()

	var buf bytes.Buffer
	info := buildStartupInfo(repo, nil, "abc")
	if err := audit.EmitStartup(&buf, info); err != nil {
		t.Fatal(err)
	}
	out := strings.TrimRight(buf.String(), "\n")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"event", "version", "repo_root", "max_files", "loaded_files", "grammars", "lsp", "binary_sha256"} {
		if _, ok := got[key]; !ok {
			t.Errorf("startup line missing field %q", key)
		}
	}
}

// TestWarnInstallTrustChain covers the four TOFU status branches.
// "missing", "match", "unknown_version" stay silent; only
// "mismatch" writes a warning. "dev" builds never check (the TOFU
// is meaningless when no release version is stamped).
func TestWarnInstallTrustChain(t *testing.T) {
	cases := []struct {
		name           string
		version        string
		tofu           string // file contents; "" omits writing the file
		sha            string
		wantWarningSub string // substring expected in stderr
	}{
		{
			name:           "missing",
			version:        "v0.1.0",
			sha:            "abc",
			wantWarningSub: "",
		},
		{
			name:           "match",
			version:        "v0.1.0",
			tofu:           "v0.1.0 abc\n",
			sha:            "abc",
			wantWarningSub: "",
		},
		{
			name:           "unknown_version",
			version:        "v0.2.0",
			tofu:           "v0.1.0 abc\n",
			sha:            "def",
			wantWarningSub: "",
		},
		{
			name:           "mismatch",
			version:        "v0.1.0",
			tofu:           "v0.1.0 expected\n",
			sha:            "actual",
			wantWarningSub: "WARNING",
		},
		{
			name:           "dev_version_skips_check",
			version:        "dev",
			tofu:           "v0.1.0 whatever\n",
			sha:            "actual",
			wantWarningSub: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// audit.KnownGoodPath returns <XDG_DATA_HOME>/yactt/known-good
			// — honour the same layout in the test.
			tofuPath := filepath.Join(dir, "yactt", "known-good")
			if tc.tofu != "" {
				if err := os.MkdirAll(filepath.Dir(tofuPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tofuPath, []byte(tc.tofu), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("XDG_DATA_HOME", dir)

			// Capture stderr.
			origStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w
			defer func() { os.Stderr = origStderr }()

			warnInstallTrustChain(tc.version, tc.sha)
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			_, _ = got.ReadFrom(r)
			out := got.String()
			if tc.wantWarningSub == "" {
				if out != "" {
					t.Errorf("expected no warning, got %q", out)
				}
				return
			}
			if !strings.Contains(out, tc.wantWarningSub) {
				t.Errorf("warning missing %q: %q", tc.wantWarningSub, out)
			}
		})
	}
}

// TestBinaryPath verifies the helper returns a non-empty absolute
// path under os.Executable. We don't pin a specific value — the
// exact path is platform-dependent — only that it resolves.
func TestBinaryPath(t *testing.T) {
	got := binaryPath()
	if got == "" {
		t.Fatal("binaryPath returned empty string")
	}
	if !filepath.IsAbs(got) && !strings.HasPrefix(got, os.Args[0]) {
		t.Errorf("binaryPath = %q, want absolute or matching os.Args[0]", got)
	}
}