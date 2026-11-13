package dolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltversion"
)

// TestCLIExecTimeoutDuration verifies BEADS_CLI_TRANSFER_TIMEOUT parsing:
// valid durations and bare seconds are honored, unset/invalid/non-positive
// values fall back to the compiled-in default.
func TestCLIExecTimeoutDuration(t *testing.T) {
	t.Run("duration string", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "20m")
		if got := cliExecTimeoutDuration(); got != 20*time.Minute {
			t.Fatalf("cliExecTimeoutDuration() = %v, want 20m", got)
		}
	})
	t.Run("bare seconds", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "90")
		if got := cliExecTimeoutDuration(); got != 90*time.Second {
			t.Fatalf("cliExecTimeoutDuration() = %v, want 90s", got)
		}
	})
	t.Run("unset falls back", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "")
		if got := cliExecTimeoutDuration(); got != cliExecTimeout {
			t.Fatalf("cliExecTimeoutDuration() = %v, want default %v", got, cliExecTimeout)
		}
	})
	t.Run("invalid falls back", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "soon")
		if got := cliExecTimeoutDuration(); got != cliExecTimeout {
			t.Fatalf("cliExecTimeoutDuration() = %v, want default %v", got, cliExecTimeout)
		}
	})
	t.Run("non-positive falls back", func(t *testing.T) {
		t.Setenv(cliExecTimeoutEnv, "-5s")
		if got := cliExecTimeoutDuration(); got != cliExecTimeout {
			t.Fatalf("cliExecTimeoutDuration() = %v, want default %v", got, cliExecTimeout)
		}
	})
}

// TestPrepareDoltCLITransferCommandSetsDeadlineAndWaitDelay pins the two
// guards against indefinite transfer hangs: the returned context carries the
// configured deadline, and the command has a non-zero WaitDelay so Wait cannot
// block forever on inherited pipes after a context kill.
func TestPrepareDoltCLITransferCommandSetsDeadlineAndWaitDelay(t *testing.T) {
	withNoDoltOnPath(t)
	cmd, ctx, cancel, err := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, "push", "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if cmd.WaitDelay == 0 {
		t.Fatal("cmd.WaitDelay = 0; a grandchild holding the output pipes would block Wait forever after a context kill")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("returned context has no deadline; transfer would be unbounded")
	}
	if remaining := time.Until(deadline); remaining > cliExecTimeout {
		t.Fatalf("deadline %v from now exceeds cliExecTimeout %v", remaining, cliExecTimeout)
	}
}

// TestCLITransferTimeoutUnblocksDespiteGrandchildPipes reproduces the wy-5f2u7
// hang shape: the direct child is killed at the context deadline, but a
// grandchild (stand-in for a cloud credential helper) still holds the
// inherited stdout/stderr pipes. Without WaitDelay, CombinedOutput blocks
// until the grandchild exits (60s here); with it, the call returns promptly.
func TestCLITransferTimeoutUnblocksDespiteGrandchildPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	binDir := t.TempDir()
	// Fake dolt: report a version at the floor (the transfer checks it
	// first), else background a pipe-holding grandchild, then hang forever.
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'dolt version " + doltversion.RequiredMin.String() + "'; exit 0; fi\nsleep 60 &\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(cliExecTimeoutEnv, "1s")

	cmd, ctx, cancel, err := prepareDoltCLITransferCommand(context.Background(), binDir, nil, false, "push", "gcs", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	cmd.WaitDelay = 500 * time.Millisecond // keep the test fast; default is cliExecWaitDelay

	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("CombinedOutput succeeded; want kill after timeout (out=%q)", out)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("CombinedOutput blocked %v despite 1s timeout + WaitDelay; pipe-holding grandchild kept Wait stuck", elapsed)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}
	msg := cliTransferError("dolt push", "gcs", ctx, out, err).Error()
	if !strings.Contains(msg, "timed out after 1s") || !strings.Contains(msg, cliExecTimeoutEnv) {
		t.Fatalf("timeout error not actionable: %q", msg)
	}
}

// TestCLITransferErrorPlainFailure pins that a non-timeout failure keeps the
// ordinary error shape and does not claim a timeout.
func TestCLITransferErrorPlainFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	msg := cliTransferError("dolt push", "gcs", ctx, []byte("remote not found"), errors.New("exit status 1")).Error()
	if strings.Contains(msg, "timed out") {
		t.Fatalf("non-timeout failure misreported as timeout: %q", msg)
	}
	if !strings.Contains(msg, "dolt push failed: remote not found") {
		t.Fatalf("unexpected error shape: %q", msg)
	}
}

// withNoDoltOnPath points PATH at an empty directory for t, so a test of the
// command prepareDoltCLITransferCommand builds does not depend on the host's
// dolt, whose version the function checks first (a dolt not on PATH is not
// checked).
func withNoDoltOnPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// TestPrepareDoltCLITransferCommandRefusesDoltBelowRequiredMin pins the
// binary floor on the CLI transfer route (git-protocol, credentialed,
// cloud-auth and local remotes): a dolt on PATH whose `dolt version` reports
// a release below doltversion.RequiredMin is refused with the floor's error
// before push, pull or fetch runs, like every other `dolt` bd runs (when
// bd's own server holds the database, dolt hands the transfer to that
// server; otherwise the binary's own engine runs it). A dolt at the minimum
// is let through.
func TestPrepareDoltCLITransferCommandRefusesDoltBelowRequiredMin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the dolt stub is a POSIX shell script")
	}
	calls := filepath.Join(t.TempDir(), "calls")
	stubOnPath := func(t *testing.T, version string) {
		t.Helper()
		binDir := t.TempDir()
		stub := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'dolt version " + version + "'; exit 0; fi\necho \"$@\" >> '" + calls + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(stub), 0o755); err != nil { //nolint:gosec // G306: the stub must be executable
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}

	for _, args := range [][]string{{"push", "origin", "main"}, {"pull", "origin", "main"}, {"fetch", "origin"}} {
		t.Run(args[0], func(t *testing.T) {
			stubOnPath(t, "2.5.1")
			cmd, _, cancel, err := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, args...)
			if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
				if cancel != nil {
					cancel()
				}
				t.Fatalf("prepareDoltCLITransferCommand(%v) = %v, want ErrBelowRequiredMin", args, err)
			}
			if cmd != nil {
				t.Errorf("got a command %v alongside the refusal", cmd.Args)
			}
			for _, want := range []string{"dolt " + args[0], "2.5.1", "requires dolt " + doltversion.RequiredMin.String(), "Upgrade dolt"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal = %q, want it to contain %q", err, want)
				}
			}
		})
	}
	if got, err := os.ReadFile(calls); err == nil {
		t.Fatalf("the dolt stub ran %q after a refusal, want only `dolt version`", got)
	}

	t.Run("at the minimum", func(t *testing.T) {
		stubOnPath(t, doltversion.RequiredMin.String())
		cmd, _, cancel, err := prepareDoltCLITransferCommand(context.Background(), t.TempDir(), nil, false, "pull", "origin", "main")
		if err != nil {
			t.Fatalf("prepareDoltCLITransferCommand = %v, want nil for dolt %s", err, doltversion.RequiredMin)
		}
		defer cancel()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("transfer failed: %v: %s", err, out)
		}
		if got, err := os.ReadFile(calls); err != nil || !strings.Contains(string(got), "pull origin main") {
			t.Fatalf("stub calls = %q (%v), want the pull to have run", got, err)
		}
	})
}

// TestCLIRouteRefusesDoltBelowRequiredMinBeforeRunningIt pins where the CLI
// route checks the dolt on PATH: as soon as it has chosen the CLI, so the
// refusal is the first thing an old binary meets. The push routes refuse it
// before prePushFSCK's `dolt fsck` (which would run here: the CLI directory
// has a .dolt/noms), and ensureMatchingCLIRemote before `dolt remote` lists
// or edits the CLI remote.
func TestCLIRouteRefusesDoltBelowRequiredMinBeforeRunningIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the dolt stub is a POSIX shell script")
	}
	newStore := func(t *testing.T) *DoltStore {
		t.Helper()
		s := &DoltStore{dbPath: t.TempDir(), database: "beads", branch: "main"}
		if err := os.MkdirAll(filepath.Join(s.CLIDir(), ".dolt", "noms"), 0o750); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, tc := range []struct {
		name string
		run  func(*DoltStore) error
	}{
		{"push", func(s *DoltStore) error { return s.doltCLIPush(context.Background(), "origin", false, nil) }},
		{"push to peer", func(s *DoltStore) error {
			return s.doltCLIPushRefToPeer(context.Background(), "peer", "main", nil)
		}},
		{"CLI remote", func(s *DoltStore) error { return s.ensureMatchingCLIRemote("origin", "file:///nonexistent/remote", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubDoltVersionOnPath(t, "2.5.1")
			err := tc.run(newStore(t))
			if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
				t.Fatalf("%s = %v, want ErrBelowRequiredMin", tc.name, err)
			}
			if got, err := os.ReadFile(calls); err == nil {
				t.Fatalf("the dolt stub ran %q before the refusal, want only `dolt version`", got)
			}
		})
	}
}

// stubDoltVersionOnPath puts a fake dolt first on PATH for t: it answers
// `dolt version` with version and records every other invocation in the
// returned calls file.
func stubDoltVersionOnPath(t *testing.T, version string) (calls string) {
	t.Helper()
	binDir := t.TempDir()
	calls = filepath.Join(t.TempDir(), "calls")
	stub := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'dolt version " + version + "'; exit 0; fi\necho \"$@\" >> '" + calls + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(stub), 0o755); err != nil { //nolint:gosec // G306: the stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// syntheticToken is an obviously fake credential for the redaction tests; no
// real secret is involved.
const syntheticToken = "ghp_TESTTOKEN"

// tokenRemote is the CI-style sync.remote whose rejection output leaked.
const tokenRemote = "git+https://x-access-token:" + syntheticToken + "@github.com/org/repo"

// assertNoCredentials fails t if msg carries the synthetic token or the
// x-access-token userinfo it was paired with.
func assertNoCredentials(t *testing.T, msg string) {
	t.Helper()
	for _, secret := range []string{syntheticToken, "x-access-token", "hunter2"} {
		if strings.Contains(msg, secret) {
			t.Errorf("error leaks %q: %s", secret, msg)
		}
	}
}

// TestCLITransferErrorRedactsRemoteCredentials pins that a failed CLI transfer
// never echoes URL userinfo from dolt's output or the operation into the
// wrapped error: a non-fast-forward rejection echoes the full stored remote,
// and the error reaches the terminal, agent transcripts and auto-push warnings.
// Host, path, the rejection detail and the cause must survive.
func TestCLITransferErrorRedactsRemoteCredentials(t *testing.T) {
	out := []byte("To " + tokenRemote + "\n ! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs to '" + tokenRemote + "'\n")

	// The operation shapes of every caller: doltCLIPush, doltCLIPull and the
	// federation peer push and fetch, whose op names the peer argument.
	for name, op := range map[string]string{
		"push":       "dolt push",
		"fetch":      "dolt fetch",
		"peer push":  "push to peer " + tokenRemote,
		"peer fetch": "fetch from peer " + tokenRemote,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			cause := errors.New("exit status 1")

			err := cliTransferError(op, "origin", ctx, out, cause)
			msg := err.Error()
			assertNoCredentials(t, msg)
			for _, keep := range []string{"git+https://github.com/org/repo", "non-fast-forward", "failed to push some refs", "exit status 1"} {
				if !strings.Contains(msg, keep) {
					t.Errorf("error lost %q: %s", keep, msg)
				}
			}
			if !errors.Is(err, cause) {
				t.Errorf("errors.Is(err, cause) = false; cause no longer wrapped: %v", err)
			}
		})
	}
}

// TestCLITransferErrorRedactsEchoedRemote is the triage reproduction: dolt
// echoes the credential-bearing remote on its "To" line and in the rejection,
// and neither the token nor its userinfo may reach the wrapped error.
func TestCLITransferErrorRedactsEchoedRemote(t *testing.T) {
	const token = "fake-tok-42"
	remote := "git+https://x-access-token:" + token + "@github.com/org/repo"
	out := []byte("To " + remote + "\n ! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs to '" + remote + "'\n")
	cause := errors.New("exit status 1")

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	for name, ctx := range map[string]context.Context{"failure": context.Background(), "timeout": expired} {
		msg := cliTransferError("dolt push", remote, ctx, out, cause).Error()
		if strings.Contains(msg, token) || strings.Contains(msg, "x-access-token") {
			t.Errorf("%s: error leaks credentials: %s", name, msg)
		}
		if !strings.Contains(msg, "github.com/org/repo") || !strings.Contains(msg, "non-fast-forward") {
			t.Errorf("%s: error lost host or cause: %s", name, msg)
		}
	}
}

// TestCLITransferErrorRedactsRejectionKeepsDetail reproduces the reported
// leak: a rejected push to a credential-bearing git+https remote makes dolt
// echo the full URL, userinfo included. Host, ref and cause must survive;
// username and token must not.
func TestCLITransferErrorRedactsRejectionKeepsDetail(t *testing.T) {
	const (
		user  = "x-access-token"
		token = "SYNTHETIC-fixture-token-7f3a9c"
	)
	remote := "git+https://" + user + ":" + token + "@github.com/example/repo"
	out := []byte(" ! [rejected]        main -> main (non-fast-forward)\n" +
		"error: failed to push some refs to '" + remote + "'\n" +
		"hint: Updates were rejected because the tip of your current branch is behind\n")
	runErr := errors.New("exit status 1")

	assertRedacted := func(t *testing.T, err error) {
		t.Helper()
		msg := err.Error()
		for _, secret := range []string{user, token} {
			if strings.Contains(msg, secret) {
				t.Errorf("error leaks %q: %s", secret, msg)
			}
		}
		for _, kept := range []string{"github.com/example/repo", "non-fast-forward", "failed to push some refs", "exit status 1"} {
			if !strings.Contains(msg, kept) {
				t.Errorf("error lost actionable detail %q: %s", kept, msg)
			}
		}
		if !errors.Is(err, runErr) {
			t.Errorf("errors.Is(err, runErr) = false: %v", err)
		}
	}

	t.Run("ordinary rejection", func(t *testing.T) {
		assertRedacted(t, cliTransferError("dolt push", remote, context.Background(), out, runErr))
	})
	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		assertRedacted(t, cliTransferError("dolt pull", remote, ctx, out, runErr))
	})
	t.Run("credential-free output is unchanged", func(t *testing.T) {
		plain := []byte("error: failed to push some refs to 'https://github.com/example/repo'\n")
		err := cliTransferError("dolt push", "https://github.com/example/repo", context.Background(), plain, runErr)
		if !strings.Contains(err.Error(), "'https://github.com/example/repo'") {
			t.Errorf("credential-free URL was altered: %s", err)
		}
		if !errors.Is(err, runErr) {
			t.Errorf("errors.Is(err, runErr) = false: %v", err)
		}
	})
}

// TestCLITransferErrorTimeoutRedactsCredentials pins the timeout branch, which
// additionally names the remote: credentials placed independently in the
// operation, the remote and dolt's output must all be absent, while the
// configured timeout, the override guidance and the cause survive.
func TestCLITransferErrorTimeoutRedactsCredentials(t *testing.T) {
	t.Setenv(cliExecTimeoutEnv, "7m")
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cause := &os.PathError{Op: "wait", Path: "dolt", Err: errors.New("signal: killed")}

	for name, tc := range map[string]struct{ op, remote, out string }{
		"in op":     {op: "push to peer " + tokenRemote, remote: "peer", out: "context deadline exceeded"},
		"in remote": {op: "dolt push", remote: tokenRemote, out: "context deadline exceeded"},
		"in out":    {op: "dolt pull", remote: "origin", out: "fetching " + tokenRemote + "\nssh://alice:hunter2@dolt.example.com/org/db stalled"},
	} {
		t.Run(name, func(t *testing.T) {
			err := cliTransferError(tc.op, tc.remote, expired, []byte(tc.out), cause)
			msg := err.Error()
			assertNoCredentials(t, msg)
			for _, keep := range []string{"timed out after 7m0s", cliExecTimeoutEnv + "=<duration>", "signal: killed"} {
				if !strings.Contains(msg, keep) {
					t.Errorf("error lost %q: %s", keep, msg)
				}
			}
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) || pathErr != cause {
				t.Errorf("errors.As did not recover the cause: %v", err)
			}
		})
	}
}

// stubTransferCause is a typed cause for asserting that wrapped transfer
// errors stay discoverable through errors.As.
type stubTransferCause struct{ msg string }

func (c stubTransferCause) Error() string { return c.msg }

// TestCLITransferErrorTimeoutRedactsAllInputs pins the timeout branch of
// cliTransferError: with an expired transfer context, credential-bearing URLs
// in the operation description, the remote and the subprocess output must all
// be redacted, while the configured timeout duration, the
// BEADS_CLI_TRANSFER_TIMEOUT guidance and the cause stay intact.
func TestCLITransferErrorTimeoutRedactsAllInputs(t *testing.T) {
	t.Setenv(cliExecTimeoutEnv, "45s")
	const token = "ghp_TESTTOKEN"
	const pass = "hunter2"
	opURL := "git+https://deploy:" + token + "@github.com/org/repo"
	remoteURL := "ssh://alice:" + pass + "@dolt.example.com/org/db"
	out := []byte("  pushing to " + opURL + " timed out on the wire  \n")
	cause := stubTransferCause{"signal: killed"}

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	for name, op := range map[string]string{
		"dolt push": "dolt push",
		"peer push": "push to peer " + opURL,
	} {
		t.Run(name, func(t *testing.T) {
			err := cliTransferError(op, remoteURL, expired, out, cause)
			msg := err.Error()
			for _, leak := range []string{token, "deploy:", pass, "alice:"} {
				if strings.Contains(msg, leak) {
					t.Errorf("timeout error leaks %q: %s", leak, msg)
				}
			}
			for _, want := range []string{
				"timed out after 45s",
				cliExecTimeoutEnv + "=<duration>",
				`to "ssh://alice@dolt.example.com/org/db"`,
				"pushing to git+https://github.com/org/repo timed out on the wire",
				"signal: killed",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("timeout error lost %q: %s", want, msg)
				}
			}
			var unwrapped stubTransferCause
			if !errors.As(err, &unwrapped) {
				t.Errorf("cause not discoverable through errors.As: %v", err)
			}
		})
	}
}

// TestCLITransferErrorCredentialFreeUnchanged pins that redaction leaves
// credential-free inputs byte-identical to the pre-redaction formatting on
// both branches (plain remote names, scp-style remotes, URLs without
// userinfo), with surrounding whitespace still trimmed from dolt's output and
// the cause still wrapped.
func TestCLITransferErrorCredentialFreeUnchanged(t *testing.T) {
	t.Setenv(cliExecTimeoutEnv, "7m")
	live, cancelLive := context.WithTimeout(context.Background(), time.Hour)
	defer cancelLive()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	cause := errors.New("exit status 1")
	const guidance = " timed out after 7m0s (override with BEADS_CLI_TRANSFER_TIMEOUT=<duration>; large transfers to cloud remotes can run long, and a busy dolt sql-server serving the database directory can stall CLI transfers): "

	tests := []struct {
		name   string
		ctx    context.Context
		op     string
		remote string
		out    string
		want   string
	}{
		{
			name: "plain name failure", ctx: live, op: "dolt push", remote: "origin",
			out:  "\n  To git+https://github.com/org/repo\n ! [rejected] main -> main (non-fast-forward)\n",
			want: "dolt push failed: To git+https://github.com/org/repo\n ! [rejected] main -> main (non-fast-forward): exit status 1",
		},
		{
			name: "scp peer failure", ctx: live, op: "fetch from peer git@github.com:org/repo.git", remote: "git@github.com:org/repo.git",
			out:  "fatal: git@github.com:org/repo.git does not appear to be a git repository\n",
			want: "fetch from peer git@github.com:org/repo.git failed: fatal: git@github.com:org/repo.git does not appear to be a git repository: exit status 1",
		},
		{
			name: "plain name timeout", ctx: expired, op: "dolt pull", remote: "origin",
			out:  "  stalled\n",
			want: `dolt pull to "origin"` + guidance + "stalled: exit status 1",
		},
		{
			name: "url without userinfo timeout", ctx: expired, op: "push to peer https://github.com/org/repo", remote: "https://github.com/org/repo",
			out:  "",
			want: `push to peer https://github.com/org/repo to "https://github.com/org/repo"` + guidance + ": exit status 1",
		},
		{
			name: "scp timeout", ctx: expired, op: "dolt push", remote: "git@github.com:org/repo.git",
			out:  "context deadline exceeded",
			want: `dolt push to "git@github.com:org/repo.git"` + guidance + "context deadline exceeded: exit status 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cliTransferError(tt.op, tt.remote, tt.ctx, []byte(tt.out), cause)
			if got := err.Error(); got != tt.want {
				t.Errorf("cliTransferError() =\n%q\nwant\n%q", got, tt.want)
			}
			if !errors.Is(err, cause) {
				t.Errorf("errors.Is(err, cause) = false: %v", err)
			}
		})
	}
}

// TestCLITransferErrorRedactsApostropheInPassword pins a password holding a
// raw "'", which used to end the URL match inside the single quotes dolt puts
// around the rejected remote, so the whole password was echoed. Both branches
// must drop it from the operation, the remote and dolt's output, keep the
// quotes and the rejection detail, and still wrap the cause.
func TestCLITransferErrorRedactsApostropheInPassword(t *testing.T) {
	t.Setenv(cliExecTimeoutEnv, "7m")
	const remote = "git+https://user:canary'secret@github.com/org/repo"
	out := []byte(" ! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs to '" + remote + "'\n")
	const detail = "! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs to 'git+https://github.com/org/repo': exit status 1"
	cause := errors.New("exit status 1")
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"failure": {
			ctx:  context.Background(),
			want: "push to peer git+https://github.com/org/repo failed: " + detail,
		},
		"timeout": {
			ctx:  expired,
			want: `push to peer git+https://github.com/org/repo to "git+https://github.com/org/repo" timed out after 7m0s (override with BEADS_CLI_TRANSFER_TIMEOUT=<duration>; large transfers to cloud remotes can run long, and a busy dolt sql-server serving the database directory can stall CLI transfers): ` + detail,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := cliTransferError("push to peer "+remote, remote, tc.ctx, out, cause)
			if got := err.Error(); got != tc.want {
				t.Errorf("cliTransferError() =\n%q\nwant\n%q", got, tc.want)
			}
			if !errors.Is(err, cause) {
				t.Errorf("errors.Is(err, cause) = false: %v", err)
			}
		})
	}
}
