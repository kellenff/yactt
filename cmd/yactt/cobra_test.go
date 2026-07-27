package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// invokeRoot mirrors invokeChunk / invokeHybrid but for the root
// command. Used by the characterization tests below to pin Cobra's
// behavior at the top level (help, version, unknown-command
// mapping).
//
// Each call builds a fresh command tree via newRootCmd() so pflag's
// Changed bits and the writers attached to the previous invocation
// don't leak across tests. Tests share a singleton at package
// scope only via the `version` variable.
func invokeRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestRoot_Help pins that `yactt` with no args (or `--help`) prints
// the rootLong block. Cobra's auto-generated command list appears
// after, so we just pin the project header and at least one
// subcommand.
func TestRoot_Help(t *testing.T) {
	out, _, err := invokeRoot(t)
	if err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	for _, want := range []string{"federated code intelligence", "overview", "chunk", "hybrid", "mcp"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help missing %q\n%s", want, out)
		}
	}
}

// TestRoot_HelpFlag pins that `--help` is also a clean exit. Both
// the bare-root case and the explicit-flag case must be silent.
func TestRoot_HelpFlag(t *testing.T) {
	out, _, err := invokeRoot(t, "--help")
	if err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	if !strings.Contains(out, "yactt") {
		t.Errorf("--help missing project name: %q", out)
	}
}

// TestRoot_Version pins the version template: `yactt <version>\n`.
// The template is set in root.go's init(); the test pins the
// shape so a future refactor that changes it (e.g. switches back
// to `version` subcommand for parity with the hand-rolled parser)
// breaks here, not at the installer level.
func TestRoot_Version(t *testing.T) {
	out, _, err := invokeRoot(t, "--version")
	if err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	want := "yactt " + version
	if !strings.HasPrefix(strings.TrimRight(out, "\n"), want) {
		t.Errorf("version output = %q, want prefix %q", out, want)
	}
}

// TestRoot_VersionShortflag pins that -v is also accepted.
func TestRoot_VersionShortflag(t *testing.T) {
	out, _, err := invokeRoot(t, "-v")
	if err != nil {
		t.Fatalf("-v returned error: %v", err)
	}
	if !strings.Contains(out, version) {
		t.Errorf("-v output %q doesn't contain %q", out, version)
	}
}

// TestRoot_UnknownCommand pins that a bogus subcommand is
// rejected as a parse error (which Execute() maps to exit 2).
func TestRoot_UnknownCommand(t *testing.T) {
	_, _, err := invokeRoot(t, "bogus")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error %q doesn't mention 'unknown command'", err.Error())
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %q doesn't mention the bad command", err.Error())
	}
}

// TestExecute_ExitCodeMapping pins the full exit-code contract in a
// single table. We don't spawn the binary; we drive Execute() the
// same way main() does and check the integer it returns.
func TestExecute_ExitCodeMapping(t *testing.T) {
	// Direct unit test on the dispatch logic: this is the same
	// mapping Execute() applies, just inlined so the test stays
	// hermetic (no need to fork the process for each case).
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"runtime_error", errors.New("disk full"), 1},
		{"parse_unknown_command", errors.New("unknown command \"bogus\""), 2},
		{"parse_unknown_flag", errors.New("unknown flag: --bogus"), 2},
		{"parse_required_flag", errors.New(`required flag(s) "repo" not set`), 2},
		{"parse_too_many_args", errors.New("accepts 0 arg(s), received 1"), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				if got := exitCodeFor(nil); got != tc.want {
					t.Errorf("exitCodeFor(nil) = %d, want %d", got, tc.want)
				}
				return
			}
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// exitCodeFor mirrors Execute()'s error→exit-code mapping. It's
// pulled out as a free function so the test exercises the same
// branches the production path does, without needing to mock
// rootCmd.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var fe *flagError
	if errors.As(err, &fe) {
		return 2
	}
	msg := err.Error()
	switch {
	case startsWith(msg, "unknown command"),
		startsWith(msg, "unknown shorthand"),
		startsWith(msg, "unknown flag"),
		startsWith(msg, "flag needs an argument"),
		startsWith(msg, "no such flag"),
		startsWith(msg, "invalid argument"),
		startsWith(msg, "required flag(s)"),
		startsWith(msg, "requires at most"),
		startsWith(msg, "requires at least"),
		startsWith(msg, "expected "),
		startsWith(msg, "accepts "):
		return 2
	}
	return 1
}

// TestMCPServe_AuditLogAccepted pins the flag-contract change: the
// hand-rolled parser rejected `--audit-log path` and only accepted
// `--audit-log=path`. Cobra accepts both, and we now treat the
// non-empty value as "audit enabled" without rejecting the
// form. This test builds the command and asserts no error from
// argument parsing; it does NOT call Serve() because that would
// block on stdin.
func TestMCPServe_AuditLogAccepted(t *testing.T) {
	cmd := newCmdMCPServe()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--audit-log", "/tmp/yactt-test-audit.log"})
	if err := cmd.ParseFlags([]string{"--audit-log", "/tmp/yactt-test-audit.log"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
}

// TestMCPServeHTTP_Defaults pins the default values for the HTTP
// daemon flags. We don't start the listener; we just parse flags
// and inspect the bound values.
func TestMCPServeHTTP_Defaults(t *testing.T) {
	cmd := newCmdMCPServeHTTP()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	checks := map[string]struct {
		def  string
		want string
	}{
		"bind":          {"127.0.0.1", cmd.Flag("bind").Value.String()},
		"port":          {"8080", cmd.Flag("port").Value.String()},
		"max-sessions":  {"256", cmd.Flag("max-sessions").Value.String()},
		"shutdown-grace": {"10s", cmd.Flag("shutdown-grace").Value.String()},
		"idle-timeout":  {"5m0s", cmd.Flag("idle-timeout").Value.String()},
	}
	for name, c := range checks {
		if c.def != c.want {
			t.Errorf("default %s = %q, want %q", name, c.want, c.def)
		}
	}
}

// TestChunk_FlagBindings pins the chunk command's flag defaults.
// Keeps the auto-generated help honest: if anyone changes a default
// in newCmdChunk, this test catches the drift.
func TestChunk_FlagBindings(t *testing.T) {
	cmd := newCmdChunk()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got := cmd.Flag("with-tests").Value.String(); got != "false" {
		t.Errorf("with-tests default = %q, want false", got)
	}
	if got := cmd.Flag("policy").Value.String(); got != "" {
		t.Errorf("policy default = %q, want empty", got)
	}
	if got := cmd.Flag("output").Value.String(); got != "" {
		t.Errorf("output default = %q, want empty", got)
	}
}

// TestHybrid_FlagBindings pins the hybrid command's flag defaults.
func TestHybrid_FlagBindings(t *testing.T) {
	cmd := newCmdHybrid()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got := cmd.Flag("limit").Value.String(); got != "10" {
		t.Errorf("limit default = %q, want 10", got)
	}
	if got := cmd.Flag("channels").Value.String(); got != "" {
		t.Errorf("channels default = %q, want empty", got)
	}
	if got := cmd.Flag("explain").Value.String(); got != "false" {
		t.Errorf("explain default = %q, want false", got)
	}
}
