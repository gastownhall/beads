// exit_codes_init_test.go — protocol v0 §E3 (documented-stable exit codes).
//
// Beyond 0 (success) and 1 (general failure), v0 freezes four exit codes as
// scriptable API:
//
//	10  init-safety: remote divergence refused
//	11  init-safety: local data exists, refused
//	12  init-safety: destroy-token missing or wrong
//	130 interactive prompt canceled (SIGINT)
//
// The clause is about the NUMBERS, not about bd's Go constants, so these tests
// assert the literals: a conforming implementation with a different internal
// spelling still has to exit 10 when it refuses a divergent init. New stable
// codes require a spec revision.
//
// The refusal codes (10/11/12) are cheap to drive: `bd init` refuses before it
// opens any store, so those tests need only a built binary and a temp git repo.
// 130 is the exception — cancellation is only reachable on the success path, so
// that test pays a full store creation before the prompt it interrupts.
package protocol

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Literal exit codes frozen by §E3. Deliberately NOT bd's Go constants: the
// corpus pins the wire contract an agent scripts against.
const (
	exitRemoteDivergenceRefused = 10
	exitLocalExistsRefused      = 11
	exitDestroyTokenMissing     = 12
	exitPromptCanceled          = 130
)

// initEnv is a clean environment for a bd init subprocess. It strips any
// ambient beads workspace so the init under test resolves only from the
// process's working directory, and pins the test Dolt server when one is
// running so a successful init can never reach a real database.
//
// home MUST NOT be the repo the init runs in: bd keeps a user-level ~/.beads
// alongside the workspace-level one, so pointing HOME at the repo would make
// "did the refusal create .beads/?" unanswerable.
func initEnv(home string, extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "xdg-config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"BEADS_TEST_MODE=1",
		// See the note in helpers_test.go env(): the detached metrics child
		// writes under HOME after bd exits and races t.TempDir() cleanup.
		"BD_DISABLE_METRICS=1",
		"BEADS_DB=",
		"BEADS_DIR=",
	}
	if testDoltPort > 0 {
		env = append(env, "BEADS_DOLT_PORT="+strconv.Itoa(testDoltPort))
	}
	if v := os.Getenv("TMPDIR"); v != "" {
		env = append(env, "TMPDIR="+v)
	}
	return append(env, extra...)
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = initEnv(t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// cloneOfRemoteWithDoltData returns a fresh, un-initialized git repo whose
// origin advertises refs/dolt/data — the state that makes `bd init` refuse a
// local-source init rather than orphan the remote's history.
func cloneOfRemoteWithDoltData(t *testing.T) string {
	t.Helper()

	bare := filepath.Join(t.TempDir(), "bare.git")
	gitIn(t, "", "init", "--bare", bare)

	source := t.TempDir()
	gitIn(t, source, "init", "-b", "main")
	gitIn(t, source, "config", "user.email", "test@protocol.test")
	gitIn(t, source, "config", "user.name", "protocol-test")
	gitIn(t, source, "commit", "--allow-empty", "-m", "init")
	gitIn(t, source, "remote", "add", "origin", bare)
	gitIn(t, source, "push", "origin", "main")
	gitIn(t, source, "push", "origin", "HEAD:refs/dolt/data")

	clone := t.TempDir()
	gitIn(t, clone, "init", "-b", "main")
	gitIn(t, clone, "remote", "add", "origin", bare)
	gitIn(t, clone, "config", "core.hooksPath", ".git/hooks")
	return clone
}

// runInitExpectRefusal runs bd init in dir and returns combined output + exit
// code, requiring a non-zero exit.
func runInitExpectRefusal(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(buildBD(t), append([]string{"init"}, args...)...)
	cmd.Dir = dir
	cmd.Env = initEnv(t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd init %s: expected refusal, got success\n%s", strings.Join(args, " "), out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("bd init %s: unexpected error type: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), exitErr.ExitCode()
}

// TestProtocol_ExitCode10_RemoteDivergenceRefused pins §E3's exit 10: a
// local-source init (--force) against an origin that already advertises
// refs/dolt/data must refuse with 10, not with the generic failure code 1 — a
// CI script branches on the difference between "you would have orphaned remote
// history" and "something went wrong".
func TestProtocol_ExitCode10_RemoteDivergenceRefused(t *testing.T) {
	t.Parallel()
	clone := cloneOfRemoteWithDoltData(t)

	out, code := runInitExpectRefusal(t, clone,
		"--force", "--prefix", "pe3", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	if code != exitRemoteDivergenceRefused {
		t.Errorf("exit code = %d, want %d (§E3 remote-divergence refusal)\n%s", code, exitRemoteDivergenceRefused, out)
	}

	// A refusal must not half-init the workspace.
	if _, err := os.Stat(filepath.Join(clone, ".beads")); err == nil {
		t.Errorf("refusal created .beads/ — refusal must be a no-op")
	}
}

// TestProtocol_ExitCode12_DestroyTokenMissing pins §E3's exit 12: authorizing
// the cross-boundary operation (--discard-remote) without the destroy token, in
// non-interactive mode, is its own refusal class — distinct from 10, so an
// agent can tell "you must authorize this" from "you must supply the token".
func TestProtocol_ExitCode12_DestroyTokenMissing(t *testing.T) {
	t.Parallel()
	clone := cloneOfRemoteWithDoltData(t)

	out, code := runInitExpectRefusal(t, clone,
		"--discard-remote", "--prefix", "pe3", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	if code != exitDestroyTokenMissing {
		t.Errorf("exit code = %d, want %d (§E3 destroy-token refusal)\n%s", code, exitDestroyTokenMissing, out)
	}
	if code == exitRemoteDivergenceRefused {
		t.Errorf("destroy-token refusal collapsed into the divergence code — §E3 codes must stay distinct")
	}
}

// TestProtocol_ExitCode11_LocalExistsRefused is the §E3 gap: exit 11 is
// returned only from the INTERACTIVE typed-confirmation abort in
// `bd init --reinit-local` (cmd/bd/init.go, guarded by term.IsTerminal on
// stdin). The non-interactive branch of the same guard returns 12 instead, so
// no piped-stdin subprocess can produce 11 — pinning it needs a PTY-backed run
// helper, tracked in wy-vh5y8. Un-skip when that lands.
func TestProtocol_ExitCode11_LocalExistsRefused(t *testing.T) {
	t.Skip("exit 11 is reachable only through an interactive TTY prompt; needs a PTY harness (wy-vh5y8)")
	_ = exitLocalExistsRefused
}

// TestProtocol_ExitCode130_PromptCanceled pins §E3's exit 130: SIGINT at an
// interactive prompt exits 130 (the shell convention, 128+SIGINT), not 1. An
// agent supervising a bd subprocess uses this to tell "the operator canceled"
// from "the command failed".
func TestProtocol_ExitCode130_PromptCanceled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGINT on Windows")
	}
	t.Parallel()

	dir := t.TempDir()
	gitIn(t, dir, "init", "-b", "main")

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinW.Close() }()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer func() { _ = outR.Close() }()

	cmd := exec.Command(buildBD(t), "init", "--prefix", "pe3", "--contributor")
	cmd.Dir = dir
	cmd.Stdin = stdinR
	cmd.Stdout = outW
	cmd.Stderr = outW
	// BD_NON_INTERACTIVE=0 keeps the prompt alive in a pipe-driven subprocess;
	// without it bd would take the non-interactive path and never prompt.
	cmd.Env = initEnv(t.TempDir(), "BD_NON_INTERACTIVE=0", "CI=")

	if err := cmd.Start(); err != nil {
		t.Fatalf("start bd init: %v", err)
	}
	_ = stdinR.Close()
	_ = outW.Close()

	var (
		mu         sync.Mutex
		output     bytes.Buffer
		promptSeen = make(chan struct{})
		once       sync.Once
		readerDone = make(chan struct{})
	)
	prompts := [][]byte{
		[]byte("Continue with contributor setup? [y/N]: "),
		[]byte("Continue anyway? [y/N]: "),
	}
	go func() {
		defer close(readerDone)
		buf := make([]byte, 1024)
		for {
			n, err := outR.Read(buf)
			if n > 0 {
				mu.Lock()
				output.Write(buf[:n])
				for _, p := range prompts {
					if bytes.Contains(output.Bytes(), p) {
						once.Do(func() { close(promptSeen) })
						break
					}
				}
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		return output.String()
	}

	// The deadline has to cover a WHOLE init, not just process startup. Unlike
	// the refusal tests above, this one takes the success path: bd builds the
	// store — a real Dolt database, since initEnv pins the suite's server —
	// and only then does the contributor wizard prompt. That is tens of seconds
	// on a loaded machine, so a short deadline fires mid-init and reports a
	// missing prompt when the prompt was merely still coming.
	const promptDeadline = 120 * time.Second

	select {
	case <-promptSeen:
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("send SIGINT: %v", err)
		}
	case err := <-waitCh:
		t.Fatalf("bd init exited before prompting: %v\n%s", err, got())
	case <-time.After(promptDeadline):
		_ = cmd.Process.Kill()
		<-waitCh
		t.Fatalf("timed out waiting for the prompt\n%s", got())
	}

	err = <-waitCh
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("canceled init: expected a non-zero exit, got %v\n%s", err, got())
	}
	if code := exitErr.ExitCode(); code != exitPromptCanceled {
		t.Errorf("exit code = %d, want %d (§E3 prompt cancel)\n%s", code, exitPromptCanceled, got())
	}
}
