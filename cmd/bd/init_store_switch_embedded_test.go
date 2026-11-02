//go:build cgo

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/steveyegge/beads/internal/configfile"
)

// readInitStoreSwitchFixture returns the bytes of the workspace files a
// refused store switch must leave untouched.
func readInitStoreSwitchFixture(t *testing.T, beadsDir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, name := range []string{"metadata.json", "config.yaml"} {
		data, err := os.ReadFile(filepath.Join(beadsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = data
	}
	return files
}

// TestEmbeddedInitReinitLocalRefusesDatabaseRepoint is the residual #3495
// defect: --reinit-local with an explicit --database other than the recorded
// one moved the workspace onto a new, empty database without confirmation
// whenever the old one held zero issues, abandoning its database-stored
// configuration (types.custom). The switch must be refused without the
// destroy token, before either database is opened.
func TestEmbeddedInitReinitLocalRefusesDatabaseRepoint(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fi", "--skip-hooks", "--skip-agents")
	bdRunOK(t, bd, dir, "config", "set", "types.custom", "molecule,convoy")
	before := readInitStoreSwitchFixture(t, beadsDir)

	cmd := exec.Command(bd, "init", "--quiet", "--reinit-local", "--prefix", "fi", "--database", "other", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitDestroyTokenMissing {
		t.Fatalf("reinit onto another database without a destroy token: want exit %d, got %v\n%s", ExitDestroyTokenMissing, err, out)
	}

	after := readInitStoreSwitchFixture(t, beadsDir)
	for name, data := range before {
		if string(after[name]) != string(data) {
			t.Errorf("%s changed by a refused store switch:\nbefore:\n%s\nafter:\n%s", name, data, after[name])
		}
	}
	requireNoFile(t, filepath.Join(beadsDir, "embeddeddolt", "other"))
	if got := readBack(t, beadsDir, "fi", "types.custom", false); got != "molecule,convoy" {
		t.Errorf("types.custom in the recorded database after a refused switch: got %q, want %q", got, "molecule,convoy")
	}
}

// TestEmbeddedInitReinitLocalDestroyTokenAuthorizesRepoint: the matching
// destroy token authorizes the switch once. The workspace then records the
// new database, and the old one stays in place with its configuration, which
// is not copied into the new store.
func TestEmbeddedInitReinitLocalDestroyTokenAuthorizesRepoint(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fi", "--skip-hooks", "--skip-agents")
	bdRunOK(t, bd, dir, "config", "set", "types.custom", "molecule,convoy")

	cmd := exec.Command(bd, "init", "--quiet", "--reinit-local", "--prefix", "fi", "--database", "other",
		"--destroy-token", FormatDestroyToken("fi"), "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reinit onto another database with the destroy token: %v\n%s", err, out)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json after the switch: cfg=%v err=%v", cfg, err)
	}
	if cfg.DoltDatabase != "other" {
		t.Errorf("recorded database after an authorized switch: got %q, want %q", cfg.DoltDatabase, "other")
	}
	requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "fi"))
	requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "other"))
	if got := readBack(t, beadsDir, "fi", "types.custom", false); got != "molecule,convoy" {
		t.Errorf("types.custom in the database left behind: got %q, want %q", got, "molecule,convoy")
	}
}

// reinitSwitchFixture builds the #3495 embedded workspace shape: database
// "fi" with zero issues holding only database-stored configuration, the
// store a count-based preflight sees nothing worth protecting in.
func reinitSwitchFixture(t *testing.T, bd string) (dir, beadsDir string) {
	t.Helper()
	dir, beadsDir, _ = bdInit(t, bd, "--prefix", "fi", "--skip-hooks", "--skip-agents")
	bdRunOK(t, bd, dir, "config", "set", "types.custom", "molecule,convoy")
	if got := readBack(t, beadsDir, "fi", "types.custom", false); got != "molecule,convoy" {
		t.Fatalf("fixture: types.custom = %q, want molecule,convoy", got)
	}
	return dir, beadsDir
}

// assertAuthorizedSwitch checks the post-conditions of an authorized switch
// to database "other": the new database is recorded, both databases exist,
// and the original database's custom configuration survives untouched.
func assertAuthorizedSwitch(t *testing.T, beadsDir string) {
	t.Helper()
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json after the switch: cfg=%v err=%v", cfg, err)
	}
	if cfg.DoltDatabase != "other" {
		t.Errorf("recorded database after an authorized switch: got %q, want %q", cfg.DoltDatabase, "other")
	}
	requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "other"))
	requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "fi"))
	if got := readBack(t, beadsDir, "fi", "types.custom", false); got != "molecule,convoy" {
		t.Errorf("types.custom in the database left behind: got %q, want %q", got, "molecule,convoy")
	}
}

// runBDOnTTY runs bd args in dir on a pseudo-terminal (the typed
// confirmation keys on term.IsTerminal on stdin), waits for want to appear
// in the terminal output, types answer, and returns the full terminal output
// and exit code.
func runBDOnTTY(t *testing.T, bd, dir string, env []string, want, answer string, args ...string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no unix PTY on Windows; the interactive typed confirmation is unreachable here")
	}
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start bd %s: %v", strings.Join(args, " "), err)
	}
	defer func() { _ = ptmx.Close() }()

	var (
		mu         sync.Mutex
		output     bytes.Buffer
		promptSeen = make(chan struct{})
		once       sync.Once
		readerDone = make(chan struct{})
	)
	go func() {
		defer close(readerDone)
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				output.Write(buf[:n])
				if strings.Contains(output.String(), want) {
					once.Do(func() { close(promptSeen) })
				}
				mu.Unlock()
			}
			if readErr != nil {
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

	// The deadline covers a whole workspace inspection before the prompt,
	// not just process startup.
	const promptDeadline = 120 * time.Second
	select {
	case <-promptSeen:
	case err := <-waitCh:
		t.Fatalf("bd %s exited before %q appeared: %v\n%s", strings.Join(args, " "), want, err, got())
	case <-time.After(promptDeadline):
		_ = cmd.Process.Kill()
		<-waitCh
		t.Fatalf("timed out waiting for %q in bd %s output\n%s", want, strings.Join(args, " "), got())
	}

	if _, err := ptmx.Write([]byte(answer + "\n")); err != nil {
		t.Fatalf("write %q to PTY: %v", answer, err)
	}
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-time.After(120 * time.Second):
		_ = cmd.Process.Kill()
		<-waitCh
		t.Fatalf("bd %s did not exit after the answer %q\n%s", strings.Join(args, " "), answer, got())
	}
	// Close the master so the drain goroutine hits EOF and the output below
	// is complete.
	_ = ptmx.Close()
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
	}

	code := 0
	if waitErr != nil {
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), waitErr, got())
		}
		code = exit.ExitCode()
	}
	return got(), code
}

// TestEmbeddedInitReinitLocalAuthorizedSwitch drives the typed confirmation
// at a real terminal: the matching phrase permits the switch once (no
// count-based prompt follows) and leaves the original database and its
// custom configuration intact; a mismatched phrase refuses with exit 11
// (ExitLocalExistsRefused), not the token refusal's 12, as a no-op.
func TestEmbeddedInitReinitLocalAuthorizedSwitch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	// Deliberately NOT t.Parallel(): the PTY subtests drive a terminal with
	// prompt deadlines; serializing keeps them free of contention noise.
	bd := buildEmbeddedBD(t)

	t.Run("matching interactive phrase permits the switch once", func(t *testing.T) {
		dir, beadsDir := reinitSwitchFixture(t, bd)
		// BD_NON_INTERACTIVE=0 forces the interactive resolution; the typed
		// phrase only fires on a real terminal.
		env := append(bdEnv(dir), "BD_NON_INTERACTIVE=0")
		out, code := runBDOnTTY(t, bd, dir, env,
			"leave fi behind", "leave fi behind",
			"init", "--quiet", "--reinit-local", "--prefix", "fi", "--database", "other",
			"--skip-hooks", "--skip-agents", "--role", "maintainer")
		if code != 0 {
			t.Fatalf("authorized interactive reinit switch exited %d, want 0:\n%s", code, out)
		}
		if strings.Contains(out, "Existing issues") {
			t.Errorf("an authorized switch was followed by the count-based prompt:\n%s", out)
		}
		assertAuthorizedSwitch(t, beadsDir)
	})

	t.Run("mismatched interactive phrase refuses with 11", func(t *testing.T) {
		dir, beadsDir := reinitSwitchFixture(t, bd)
		before := readInitStoreSwitchFixture(t, beadsDir)
		env := append(bdEnv(dir), "BD_NON_INTERACTIVE=0")
		out, code := runBDOnTTY(t, bd, dir, env,
			"leave fi behind", "leave nope behind",
			"init", "--quiet", "--reinit-local", "--prefix", "fi", "--database", "other",
			"--skip-hooks", "--skip-agents")
		if code != ExitLocalExistsRefused {
			t.Fatalf("mismatched phrase exited %d, want %d (ExitLocalExistsRefused):\n%s", code, ExitLocalExistsRefused, out)
		}
		if strings.Contains(out, "Existing issues") {
			t.Errorf("the switch refusal was followed by the count-based prompt:\n%s", out)
		}
		after := readInitStoreSwitchFixture(t, beadsDir)
		for name, data := range before {
			if string(after[name]) != string(data) {
				t.Errorf("%s changed by a refused store switch:\nbefore:\n%s\nafter:\n%s", name, data, after[name])
			}
		}
		requireNoFile(t, filepath.Join(beadsDir, "embeddeddolt", "other"))
		requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "fi"))
		if got := readBack(t, beadsDir, "fi", "types.custom", false); got != "molecule,convoy" {
			t.Errorf("refused switch must preserve the recorded database's custom types, got %q", got)
		}
	})
}

// runBDOnTTYAwaitExit runs bd args in dir on a pseudo-terminal and waits for
// it to exit without anything being typed. If promptMarker appears in the
// terminal output first, the test fails: the command was required to decide
// without reading an interactive answer.
func runBDOnTTYAwaitExit(t *testing.T, bd, dir string, env []string, promptMarker string, args ...string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no unix PTY on Windows; the interactive typed confirmation is unreachable here")
	}
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start bd %s: %v", strings.Join(args, " "), err)
	}
	defer func() { _ = ptmx.Close() }()

	var (
		mu         sync.Mutex
		output     bytes.Buffer
		promptSeen = make(chan struct{})
		once       sync.Once
		readerDone = make(chan struct{})
	)
	go func() {
		defer close(readerDone)
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				output.Write(buf[:n])
				if strings.Contains(output.String(), promptMarker) {
					once.Do(func() { close(promptSeen) })
				}
				mu.Unlock()
			}
			if readErr != nil {
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

	select {
	case <-promptSeen:
		_ = cmd.Process.Kill()
		<-waitCh
		t.Fatalf("bd %s prompted for an interactive answer although non-interactive mode was resolved:\n%s", strings.Join(args, " "), got())
	case err := <-waitCh:
		_ = ptmx.Close()
		select {
		case <-readerDone:
		case <-time.After(2 * time.Second):
		}
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, got())
			}
			code = exit.ExitCode()
		}
		return got(), code
	case <-time.After(180 * time.Second):
		_ = cmd.Process.Kill()
		<-waitCh
		t.Fatalf("bd %s did not exit within the deadline\n%s", strings.Join(args, " "), got())
	}
	return "", 0 // unreachable
}

// TestEmbeddedInitReinitLocalNonInteractiveTakesTokenPath: on a terminal with
// non-interactive mode resolved (BD_NON_INTERACTIVE=1), both destructive
// confirmations take the token path and exit 12 without reading an answer.
func TestEmbeddedInitReinitLocalNonInteractiveTakesTokenPath(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	// Deliberately NOT t.Parallel(): PTY interactions with prompt deadlines.
	bd := buildEmbeddedBD(t)

	t.Run("store-switch confirmation takes the token path", func(t *testing.T) {
		dir, _ := reinitSwitchFixture(t, bd)
		env := append(bdEnv(dir), "BD_NON_INTERACTIVE=1")
		out, code := runBDOnTTYAwaitExit(t, bd, dir, env, "to confirm:",
			"init", "--quiet", "--reinit-local", "--prefix", "fi", "--database", "other",
			"--skip-hooks", "--skip-agents", "--role", "maintainer")
		if code != ExitDestroyTokenMissing {
			t.Fatalf("TTY with non-interactive mode resolved exited %d, want %d:\n%s", code, ExitDestroyTokenMissing, out)
		}
		if !strings.Contains(out, "Refusing to switch stores") {
			t.Errorf("wanted the non-interactive switch refusal, got:\n%s", out)
		}
		if strings.Contains(out, "Type 'leave") {
			t.Errorf("the refusal asked for a typed phrase although non-interactive mode was resolved:\n%s", out)
		}
	})

	t.Run("populated-store confirmation takes the token path", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fi", "--skip-hooks", "--skip-agents")
		bdRunOK(t, bd, dir, "create", "--title", "seed issue", "--type", "task")
		env := append(bdEnv(dir), "BD_NON_INTERACTIVE=1")
		// Same database, no switch: the existing populated-store confirmation.
		out, code := runBDOnTTYAwaitExit(t, bd, dir, env, "to confirm:",
			"init", "--quiet", "--reinit-local", "--prefix", "fi",
			"--skip-hooks", "--skip-agents", "--role", "maintainer")
		if code != ExitDestroyTokenMissing {
			t.Fatalf("TTY with non-interactive mode resolved exited %d, want %d:\n%s", code, ExitDestroyTokenMissing, out)
		}
		if !strings.Contains(out, "Refusing to destroy") {
			t.Errorf("wanted the non-interactive populated-store refusal, got:\n%s", out)
		}
		if strings.Contains(out, "Type 'destroy") {
			t.Errorf("the refusal asked for a typed confirmation although non-interactive mode was resolved:\n%s", out)
		}
	})
}
