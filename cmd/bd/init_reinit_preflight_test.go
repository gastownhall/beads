package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"golang.org/x/term"
)

// These tests pin the --reinit-local destroy-confirmation preflight's answer
// to GH#5628: an uncountable workspace must be confirmed, never treated as
// empty, and the count must describe the workspace the gate replaces —
// beadsDirAbs, not whatever BEADS_DIR or the FindBeadsDir walk-up would find.
// All of them are Dolt-free: they drive the count into failure through the
// workspace's metadata itself, never through a real database.

// uncountableTargetBeadsDir returns a .beads whose issues cannot be counted:
// it holds beads project files (an unloadable metadata.json), so the store
// open — and with it the count — fails, which is the shape of a slow, large or
// damaged database the preflight used to read as "nothing to destroy".
func uncountableTargetBeadsDir(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An unloadable metadata.json makes the store open (and so the count) fail.
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := countReinitTargetIssues(beadsDir); err == nil {
		t.Fatal("precondition: countReinitTargetIssues should fail on an unloadable metadata.json")
	}
	return beadsDir
}

// TestRunInitReinitPreflight_CountErrorRefuses pins that a count error refuses
// rather than silently skipping the destroy confirmation, in the same shape
// as a counted non-zero workspace: exit 12 (ExitDestroyTokenMissing), the
// target directory named, the count reported as unknown with the count error,
// and no destroy-token value in the output (ADR-0002 Invariant 4).
func TestRunInitReinitPreflight_CountErrorRefuses(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal: the non-interactive refusal this pins would be a typed prompt instead")
	}

	t.Run("refuses without a destroy token, naming the target and the error", func(t *testing.T) {
		beadsDir := uncountableTargetBeadsDir(t)
		isolateBeadsDirForTest(t)

		var err error
		stderr := captureStderr(t, func() {
			err = runInitReinitPreflight(true, beadsDir, "bd", "")
		})
		if code, ok := exitCodeFromError(err); !ok || code != ExitDestroyTokenMissing {
			t.Fatalf("runInitReinitPreflight = %v, want exit %d (ExitDestroyTokenMissing): the count error silently skipped the destroy confirmation", err, ExitDestroyTokenMissing)
		}
		if !strings.Contains(stderr, "Existing issues: unknown (could not count them:") {
			t.Errorf("stderr must report the count as unknown with the count error, got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Target: "+beadsDir) {
			t.Errorf("stderr must name the target .beads directory, got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Refusing to destroy an unknown number of issues in non-interactive mode.") {
			t.Errorf("stderr must refuse in the non-interactive shape, got:\n%s", stderr)
		}
		if strings.Contains(stderr, "DESTROY-") {
			t.Errorf("stderr must not echo the destroy token value (ADR-0002 Invariant 4), got:\n%s", stderr)
		}
	})

	t.Run("counts the gate's target, not BEADS_DIR", func(t *testing.T) {
		beadsDir := uncountableTargetBeadsDir(t)
		// BEADS_DIR names a directory that counts as zero (no project
		// files). The pre-fixed preflight resolved its workspace through
		// BEADS_DIR, so this row used to pass with no confirmation at all.
		// The refusal must be for the gate's target: the argument.
		otherDir := filepath.Join(t.TempDir(), "other", "beads")
		if err := os.MkdirAll(otherDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(otherDir, "registry.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", otherDir)

		var err error
		stderr := captureStderr(t, func() {
			err = runInitReinitPreflight(true, beadsDir, "bd", "")
		})
		if code, ok := exitCodeFromError(err); !ok || code != ExitDestroyTokenMissing {
			t.Fatalf("runInitReinitPreflight = %v, want exit %d (ExitDestroyTokenMissing) for the gate's target, not BEADS_DIR's workspace", err, ExitDestroyTokenMissing)
		}
		if !strings.Contains(stderr, "Target: "+beadsDir) {
			t.Errorf("the refusal must name the target passed in, got:\n%s", stderr)
		}
		if strings.Contains(stderr, otherDir) {
			t.Errorf("the refusal must not name the BEADS_DIR workspace, got:\n%s", stderr)
		}
	})
}

// TestRunInitReinitPreflight_DestroyTokenProceeds pins that the
// --destroy-token is the non-interactive remedy for an uncountable target
// too: with the matching token the preflight lets the re-initialization
// proceed.
func TestRunInitReinitPreflight_DestroyTokenProceeds(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal: this pins the non-interactive token path")
	}
	beadsDir := uncountableTargetBeadsDir(t)
	isolateBeadsDirForTest(t)

	var err error
	stderr := captureStderr(t, func() {
		err = runInitReinitPreflight(true, beadsDir, "bd", FormatDestroyToken("bd"))
	})
	if err != nil {
		t.Fatalf("runInitReinitPreflight with the matching destroy token = %v, want nil so the re-initialization proceeds\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "Destroy token accepted. Proceeding with re-initialization.") {
		t.Errorf("stderr must report the accepted token, got:\n%s", stderr)
	}
	if strings.Contains(stderr, "DESTROY-") {
		t.Errorf("stderr must not echo the destroy token value (ADR-0002 Invariant 4), got:\n%s", stderr)
	}
}

// TestConfirmReinitDestroy drives the destroy confirmation through injected
// streams — the same injection shape as currentAdoptPolicy — so every branch
// is pinned without a terminal and without a store: an unknown count with
// its error, and a known count, crossed with the interactive typed prompt and
// the non-interactive --destroy-token demand.
func TestConfirmReinitDestroy(t *testing.T) {
	countErr := errors.New("store open failed: count deadline exceeded")
	target := filepath.Join("t", ".beads")

	rows := []struct {
		name         string
		count        int
		countErr     error
		interactive  bool
		answer       string
		destroyToken string
		wantCode     int // 0: the confirmation proceeds (nil)
		wantOut      []string
	}{
		{
			name:     "unknown count, non-interactive, no token",
			countErr: countErr,
			wantCode: ExitDestroyTokenMissing,
			wantOut: []string{
				"Target: " + target,
				"Existing issues: unknown (could not count them: " + countErr.Error() + ")",
				"Refusing to destroy an unknown number of issues in non-interactive mode.",
				"See 'bd help init-safety' for the required --destroy-token format.",
			},
		},
		{
			name:         "unknown count, non-interactive, matching token",
			countErr:     countErr,
			destroyToken: "DESTROY-bd",
			wantOut: []string{
				"Existing issues: unknown (could not count them: " + countErr.Error() + ")",
				"Destroy token accepted. Proceeding with re-initialization.",
			},
		},
		{
			name:     "known count 3, non-interactive, no token",
			count:    3,
			wantCode: ExitDestroyTokenMissing,
			wantOut: []string{
				"Existing issues: 3",
				"Refusing to destroy 3 issues in non-interactive mode.",
			},
		},
		{
			name:         "known count 3, non-interactive, matching token",
			count:        3,
			destroyToken: "DESTROY-bd",
			wantOut: []string{
				"Existing issues: 3",
				"Destroy token accepted. Proceeding with re-initialization.",
			},
		},
		{
			name:     "unknown count with a control character is spelled, not raw",
			countErr: errors.New("dial \x1b[31m127.0.0.1:1\x1b[0m refused"),
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"could not count them:"},
		},
		{
			name:        "known count 3, interactive, typed phrase",
			count:       3,
			interactive: true,
			answer:      "destroy 3 issues",
			wantOut: []string{
				"Existing issues: 3",
				"Type 'destroy 3 issues' to confirm: ",
			},
		},
		{
			name:        "known count 3, interactive, wrong answer",
			count:       3,
			interactive: true,
			answer:      "destroy 0 issues",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Aborted. Database was NOT modified."},
		},
		{
			name:        "unknown count, interactive, typed 'destroy all issues'",
			countErr:    countErr,
			interactive: true,
			answer:      "destroy all issues",
			wantOut: []string{
				"Existing issues: unknown (could not count them: " + countErr.Error() + ")",
				"Type 'destroy all issues' to confirm: ",
			},
		},
		{
			// The phrase says "all", not a number: there is no count to type.
			name:        "unknown count, interactive, a typed count is refused",
			countErr:    countErr,
			interactive: true,
			answer:      "destroy 0 issues",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Aborted. Database was NOT modified."},
		},
		{
			name:        "unknown count, interactive, wrong answer",
			countErr:    countErr,
			interactive: true,
			answer:      "definitely not the confirmation",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Aborted. Database was NOT modified."},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var out bytes.Buffer
			err := confirmReinitDestroy(&out, strings.NewReader(row.answer), row.interactive, target,
				row.count, row.countErr, "bd", row.destroyToken)

			if row.wantCode == 0 {
				if err != nil {
					t.Fatalf("confirmReinitDestroy = %v, want nil (the confirmation proceeds)\n%s", err, out.String())
				}
			} else if code, ok := exitCodeFromError(err); !ok || code != row.wantCode {
				t.Fatalf("confirmReinitDestroy = %v, want exit %d\n%s", err, row.wantCode, out.String())
			}
			for _, want := range row.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output must contain %q, got:\n%s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "\x1b[") {
				t.Errorf("output must spell control characters, not print them raw, got:\n%q", out.String())
			}
			if strings.Contains(out.String(), "DESTROY-") {
				t.Errorf("output must not echo the destroy token value (ADR-0002 Invariant 4), got:\n%s", out.String())
			}
		})
	}
}

// TestRunInitReinitPreflight_WorktreeCountsMainCheckout pins GH#5628 part 2,
// the linked-worktree shape: from a worktree, `bd init --reinit-local`
// replaces the MAIN checkout's .beads (the directory the gate resolves via
// GetWorktreeFallbackBeadsDir), so the preflight must count — and refuse on —
// that database, not the worktree's own .beads. The worktree's .beads here
// holds nothing countable (registry files only), the shape that used to give
// "0 issues" confidence while the main checkout's database was about to be
// destroyed, and still would if the preflight resolved its workspace the way
// the count it replaced did (BEADS_DIR, then the FindBeadsDir walk-up).
//
// Deliberately not t.Parallel(): t.Chdir and a shared git worktree fixture.
func TestRunInitReinitPreflight_WorktreeCountsMainCheckout(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal: the non-interactive refusal this pins would be a typed prompt instead")
	}
	isolateBeadsDirForTest(t)

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	dir := t.TempDir()
	mainRepo := filepath.Join(dir, "main-repo")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	run(mainRepo, "init")
	run(mainRepo, "config", "user.email", "test@example.com")
	run(mainRepo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(mainRepo, "add", "README.md")
	run(mainRepo, "commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(dir, "worktree")
	cmd := gitCommand("worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		remove := gitCommand("worktree", "remove", "--force", worktreeDir)
		remove.Dir = mainRepo
		_ = remove.Run()
	})

	// The main checkout's .beads: some beads project files, uncountable.
	mainBeadsDir := filepath.Join(mainRepo, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(mainBeadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := countReinitTargetIssues(mainBeadsDir); err == nil {
		t.Fatal("precondition: the main checkout's .beads must be uncountable")
	}
	// The worktree's own .beads: project files and an EMPTY database of its
	// own — the exact shape of the hazard. HasBeadsDatabase is true (a dolt
	// data dir), so the resolution the pre-fix count used (the FindBeadsDir
	// walk-up) prefers it over the main checkout and would have counted it —
	// zero issues — while re-init replaced the uncountable database the gate
	// targets.
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeBeadsDir, "metadata.json"), []byte(`{"backend": "dolt", "database": "dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worktreeBeadsDir, "dolt", "emptydb", ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()
	if !git.IsWorktree() {
		t.Fatal("precondition: expected git.IsWorktree() inside the linked worktree")
	}
	// The resolution the pre-fix count used — the FindBeadsDir walk-up —
	// finds the worktree's own .beads here (it holds project files and a
	// database), not the main checkout's: it would have counted the empty
	// worktree database as zero issues and skipped the confirmation while
	// re-init replaced the uncountable database at gateTarget below.
	found, err := filepath.EvalSymlinks(beads.FindBeadsDir())
	if err != nil {
		t.Fatal(err)
	}
	foundWant, err := filepath.EvalSymlinks(worktreeBeadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if found != foundWant {
		t.Fatalf("precondition: FindBeadsDir = %q, want the worktree's own %q", found, foundWant)
	}

	// The way init itself resolves beadsDirForInit with no BEADS_DIR: the
	// worktree fallback to the main checkout's .beads.
	t.Run("the preflight refuses for, and names, the main checkout's .beads", func(t *testing.T) {
		gateTarget := beads.GetWorktreeFallbackBeadsDir()
		if gateTarget == "" {
			t.Fatal("precondition: GetWorktreeFallbackBeadsDir resolved nothing in a linked worktree")
		}
		resolvedTarget, err := filepath.EvalSymlinks(gateTarget)
		if err != nil {
			t.Fatal(err)
		}
		resolvedMain, err := filepath.EvalSymlinks(mainBeadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if resolvedTarget != resolvedMain {
			t.Fatalf("precondition: the gate target %q is not the main checkout's .beads %q", resolvedTarget, resolvedMain)
		}
		var refusal error
		stderr := captureStderr(t, func() {
			refusal = runInitReinitPreflight(true, gateTarget, "bd", "")
		})
		if code, ok := exitCodeFromError(refusal); !ok || code != ExitDestroyTokenMissing {
			t.Fatalf("runInitReinitPreflight = %v, want exit %d (ExitDestroyTokenMissing): the worktree's own .beads counts as zero, but the gate's target — the main checkout's database — is uncountable", refusal, ExitDestroyTokenMissing)
		}
		if !strings.Contains(stderr, "Target: "+gateTarget) {
			t.Errorf("stderr must name the main checkout's .beads as the target, got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Refusing to destroy an unknown number of issues") {
			t.Errorf("stderr must report the unknown count, got:\n%s", stderr)
		}
		if got, err := os.ReadFile(metadataPath); err != nil || string(got) != string(beforeMetadata) {
			t.Errorf("the refusal must leave the main checkout's .beads unchanged, got %q, %v", got, err)
		}
		mainEntries, statErr := os.ReadDir(mainBeadsDir)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if len(mainEntries) != 1 || mainEntries[0].Name() != "metadata.json" {
			t.Errorf("the refusal must create nothing in the main checkout's .beads, found %d entries", len(mainEntries))
		}
	})
}

// TestCountReinitTargetIssues pins the count's own contract: which workspaces
// count as provably empty (zero, no error), and which must surface their
// errors so the preflight treats them as "may hold data".
func TestCountReinitTargetIssues(t *testing.T) {
	t.Run("an unloadable metadata.json is an error, never zero", func(t *testing.T) {
		beadsDir := uncountableTargetBeadsDir(t)
		count, err := countReinitTargetIssues(beadsDir)
		if err == nil {
			t.Fatalf("countReinitTargetIssues = (%d, nil), want an error: an unloadable store must be unknown, not empty", count)
		}
	})

	t.Run("a server-mode workspace with a local copy is an error, never absent", func(t *testing.T) {
		beadsDir := writeServerModeTargetWorkspace(t, true /* withLocalCopy */)
		count, err := countReinitTargetIssues(beadsDir)
		if err == nil {
			t.Fatalf("countReinitTargetIssues = (%d, nil), want an error: a local copy can hold data, so an unreachable server must not read as absent", count)
		}
	})

	t.Run("a server-mode workspace with an unreachable server is an error, never absent", func(t *testing.T) {
		// No local copy, but the server is not reachable either: only a
		// REACHABLE server reporting the database gone proves absence.
		beadsDir := writeServerModeTargetWorkspace(t, false /* withLocalCopy */)
		count, err := countReinitTargetIssues(beadsDir)
		if err == nil {
			t.Fatalf("countReinitTargetIssues = (%d, nil), want an error: absence is proven by a reachable server, and nothing reaches port 1", count)
		}
	})

	t.Run("a missing directory counts as zero and stays missing", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), "never-created", ".beads")
		count, err := countReinitTargetIssues(beadsDir)
		if err != nil || count != 0 {
			t.Fatalf("countReinitTargetIssues = (%d, %v), want (0, nil): a target with no .beads has nothing to destroy", count, err)
		}
		if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
			t.Errorf("the count must not create its target, stat err %v", statErr)
		}
	})

	t.Run("an empty directory counts as zero and creates nothing", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		count, err := countReinitTargetIssues(beadsDir)
		if err != nil || count != 0 {
			t.Fatalf("countReinitTargetIssues = (%d, %v), want (0, nil): empty .beads has nothing to destroy", count, err)
		}
		entries, statErr := os.ReadDir(beadsDir)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if len(entries) != 0 {
			t.Errorf("the count must open no store for a target without project files; created %d entries", len(entries))
		}
	})

	t.Run("a registry-only directory counts as zero and opens no store", func(t *testing.T) {
		// Registry files without project files are not a workspace (the
		// ~/.beads shape); discovery refuses them, and so does the count —
		// without opening a store that would create a database there.
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "registry.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		count, err := countReinitTargetIssues(beadsDir)
		if err != nil || count != 0 {
			t.Fatalf("countReinitTargetIssues = (%d, %v), want (0, nil): registry files name no workspace to destroy", count, err)
		}
		entries, statErr := os.ReadDir(beadsDir)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if len(entries) != 1 || entries[0].Name() != "registry.json" {
			t.Errorf("the count must create nothing beside the registry files, found %d entries", len(entries))
		}
	})
}

// TestRunInitReinitPreflight_NothingToDestroyProceedsSilently pins the benign
// half: a target the count proves empty passes the preflight with no warning,
// no typed prompt and no token demand, so a fresh init asks nothing.
func TestRunInitReinitPreflight_NothingToDestroyProceedsSilently(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads") // never created
	isolateBeadsDirForTest(t)

	var err error
	stderr := captureStderr(t, func() {
		err = runInitReinitPreflight(true, beadsDir, "bd", "")
	})
	if err != nil {
		t.Fatalf("runInitReinitPreflight = %v, want nil: a proven-empty target must proceed", err)
	}
	if stderr != "" {
		t.Errorf("a proven-empty target must produce no warning, prompt or token demand, got:\n%s", stderr)
	}
}

// writeServerModeTargetWorkspace writes a .beads whose metadata selects Dolt
// server mode against a port nothing listens on: the store open fails, so
// whether the count surfaces that error or rewrites it as "absent" is exactly
// what the caller is pinning. withLocalCopy also lays down a local
// <dolt>/<db>/.dolt directory, which must make "absent" false on its own: a
// local copy can hold data while the server is unreachable.
func writeServerModeTargetWorkspace(t *testing.T, withLocalCopy bool) string {
	t.Helper()
	// Nothing listens at 127.0.0.1:1, and the OS refuses the connect fast;
	// environment wins over any ambient server this host runs (the agent
	// host's own Dolt server would otherwise answer, and the count would
	// succeed or fail for reasons this fixture does not control).
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{
		"database":         "dolt",
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_database":    "myproject",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 1,
		"project_id":       "existing-0000-1111-2222-333344445555",
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if withLocalCopy {
		localDB := filepath.Join(beadsDir, "dolt", "myproject", ".dolt")
		if err := os.MkdirAll(localDB, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return beadsDir
}
