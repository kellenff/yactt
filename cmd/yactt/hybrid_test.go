package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/hybrid"
	"github.com/kellenff/yactt/internal/store"
	"github.com/kellenff/yactt/internal/store/repofixture"
)

// invokeHybrid builds a fresh hybrid command, runs it with the given
// args, and returns the error from Execute() plus the captured
// stdout/stderr. Mirrors invokeChunk in chunk_test.go so the
// argparse-level tests follow the same shape.
func invokeHybrid(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newCmdHybrid()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// loadHybridFixture mirrors loadChunkFixture: load the repofixture
// with no extras so the CLI tests share a single fixture pattern
// with the chunk CLI tests.
func loadHybridFixture(t *testing.T) *store.Repo {
	t.Helper()
	fix := repofixture.New(t)
	r, errs, err := store.Load(fix.Root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range errs {
		t.Errorf("Load per-file err: %v", e)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// TestRunHybridPipeline_DefaultOutput pins the happy path: a query
// returns a non-empty results list as JSON on stdout.
func TestRunHybridPipeline_DefaultOutput(t *testing.T) {
	r := loadHybridFixture(t)
	var stdout, stderr bytes.Buffer
	if err := runHybridPipeline(r, hybrid.Options{
		Repo:     r,
		Query:    "login",
		Limit:    5,
		Channels: hybrid.AllChannels(),
		Vector:   hybrid.NewBagOfTokens(),
	}, false, &stdout, &stderr); err != nil {
		t.Fatalf("runHybridPipeline: %v", err)
	}
	if !strings.Contains(stdout.String(), `"results"`) {
		t.Errorf("stdout missing results: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"channel": "rrf"`) {
		t.Errorf("stdout missing merged channel tag: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "hits") {
		t.Errorf("stderr missing summary: %q", stderr.String())
	}
	var got struct {
		Results []hybrid.Hit `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not valid JSON: %v\nstdout: %s", err, stdout.String())
	}
	if len(got.Results) == 0 {
		t.Fatalf("no results returned")
	}
}

// TestRunHybridPipeline_Explain pins the per-channel view.
func TestRunHybridPipeline_Explain(t *testing.T) {
	r := loadHybridFixture(t)
	var stdout, stderr bytes.Buffer
	if err := runHybridPipeline(r, hybrid.Options{
		Repo:     r,
		Query:    "login",
		Limit:    5,
		Channels: hybrid.AllChannels(),
		Vector:   hybrid.NewBagOfTokens(),
	}, true, &stdout, &stderr); err != nil {
		t.Fatalf("runHybridPipeline explain: %v", err)
	}
	for _, k := range []string{`"structural"`, `"bm25"`, `"vector"`, `"rrf"`} {
		if !strings.Contains(stdout.String(), k) {
			t.Errorf("explain output missing %s: %q", k, stdout.String())
		}
	}
}

// TestRunHybridPipeline_StructuralOnly verifies a single-channel
// run works. The merged output still tags channel="rrf" — that's
// the merge identity, not a multi-channel requirement.
func TestRunHybridPipeline_StructuralOnly(t *testing.T) {
	r := loadHybridFixture(t)
	var stdout, _ bytes.Buffer
	err := runHybridPipeline(r, hybrid.Options{
		Repo:     r,
		Query:    "Login",
		Limit:    3,
		Channels: hybrid.Channels{Structural: true},
	}, false, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runHybridPipeline: %v", err)
	}
	if !strings.Contains(stdout.String(), "Login") {
		t.Errorf("stdout missing Login hit: %q", stdout.String())
	}
}

// TestRunHybrid_RequiresRepo pins the --repo validation. Cobra
// reports the missing-required-flag error in the form
// `required flag(s) "repo" not set`.
func TestRunHybrid_RequiresRepo(t *testing.T) {
	_, _, err := invokeHybrid(t, "--query", "x")
	if err == nil {
		t.Fatal("expected error for missing --repo")
	}
	if !strings.Contains(err.Error(), "repo") {
		t.Errorf("error should mention repo, got %v", err)
	}
}

// TestRunHybrid_RequiresQuery pins the --query validation.
func TestRunHybrid_RequiresQuery(t *testing.T) {
	_, _, err := invokeHybrid(t, "--repo", "/tmp")
	if err == nil {
		t.Fatal("expected error for missing --query")
	}
	if !strings.Contains(err.Error(), "query") {
		t.Errorf("error should mention query, got %v", err)
	}
}

// TestRunHybrid_UnknownFlag pins flag validation.
func TestRunHybrid_UnknownFlag(t *testing.T) {
	_, _, err := invokeHybrid(t, "--repo", "/tmp", "--query", "x", "--bogus")
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention unknown flag bogus, got %v", err)
	}
}

// TestRunHybrid_HelpDoesNotError pins that --help is a clean exit.
// In Cobra, --help short-circuits to a help print and Execute()
// returns nil. We assert against the captured stdout/stderr buffers
// so the help text is observable here too, not just visually.
func TestRunHybrid_HelpDoesNotError(t *testing.T) {
	out, _, err := invokeHybrid(t, "--help")
	if err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	if !strings.Contains(out, "hybrid") {
		t.Errorf("--help stdout missing 'hybrid' marker: %q", out)
	}
}

// TestRunHybrid_UnknownChannel pins the channel-list validation.
// parseChannels runs in runHybrid; the error reaches Execute() as
// a returned error from RunE.
func TestRunHybrid_UnknownChannel(t *testing.T) {
	_, _, err := invokeHybrid(t,
		"--repo", "/tmp",
		"--query", "x",
		"--channels", "structural,bogus",
	)
	if err == nil {
		t.Fatal("expected error for unknown channel")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention bogus, got %v", err)
	}
}

// TestParseChannels pins the comma-separated parser.
func TestParseChannels(t *testing.T) {
	cases := []struct {
		in   string
		want hybrid.Channels
		err  bool
	}{
		{"", hybrid.AllChannels(), false},
		{"structural", hybrid.Channels{Structural: true}, false},
		{"bm25,vector", hybrid.Channels{BM25: true, Vector: true}, false},
		{"structural,bm25,vector", hybrid.AllChannels(), false},
		{"structural,", hybrid.Channels{Structural: true}, false}, // trailing empty ok
		{"bogus", hybrid.Channels{}, true},
		{",,,", hybrid.Channels{}, true}, // empty → "must include at least one"
	}
	for _, c := range cases {
		got, err := parseChannels(c.in)
		if (err != nil) != c.err {
			t.Errorf("parseChannels(%q) err = %v, want err=%v", c.in, err, c.err)
		}
		if !c.err && got != c.want {
			t.Errorf("parseChannels(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}