package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/utils"
)

// writeReinitTargetMetadata writes metadata.json into beadsDir, creating it.
func writeReinitTargetMetadata(t *testing.T, beadsDir string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// requireExitCode fails unless err is an *exitError carrying want.
func requireExitCode(t *testing.T, err error, want int) {
	t.Helper()
	var exitErr *exitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exitError with code %d, got %v", want, err)
	}
	if exitErr.Code != want {
		t.Fatalf("want exit code %d, got %d", want, exitErr.Code)
	}
}

// TestRunInitReinitPreflight_CountErrorRefuses pins GH#5628 part 1: when the
// preflight cannot count the database it is about to destroy, --reinit-local
// must refuse rather than fall through to the destructive path with no count,
// no destroy token and no confirmation.
//
// BEADS_DIR names a different workspace than the one passed in, so the
// refusal must be about the argument (the gate's target), not about whatever
// BEADS_DIR or discovery would have found.
func TestRunInitReinitPreflight_CountErrorRefuses(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, "target", ".beads")
	// An unloadable metadata.json makes the store open (and so the count) fail.
	writeReinitTargetMetadata(t, beadsDir, []byte("{not json"))
	otherDir := filepath.Join(root, "other", ".beads")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", otherDir)

	if _, err := countReinitTargetIssues(beadsDir); err == nil {
		t.Fatal("precondition: countReinitTargetIssues should fail on an unloadable metadata.json")
	}

	var err error
	out := captureStderr(t, func() {
		err = runInitReinitPreflight(true, beadsDir, "bd", "")
	})
	if err == nil {
		t.Fatalf("runInitReinitPreflight returned nil after the count failed: the destroy confirmation was silently skipped\n%s", out)
	}
	requireExitCode(t, err, ExitDestroyTokenMissing)
	if !strings.Contains(out, "Target: "+beadsDir) {
		t.Errorf("warning must name the target %s, got:\n%s", beadsDir, out)
	}
	if strings.Contains(out, otherDir) {
		t.Errorf("warning names BEADS_DIR's workspace instead of the target, got:\n%s", out)
	}
	if !strings.Contains(out, "could not count them") {
		t.Errorf("warning must report the count error, got:\n%s", out)
	}
	if strings.Contains(out, "DESTROY-") {
		t.Errorf("refusal must never print the destroy token value (ADR-0002 Invariant 4), got:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); statErr != nil {
		t.Errorf("the target must be left unchanged: %v", statErr)
	}
}

// TestConfirmReinitDestroy pins the confirmation for a known and an unknown
// issue count, interactive and not.
func TestConfirmReinitDestroy(t *testing.T) {
	const target = "/work/project/.beads"
	countErr := errors.New("open store: connection refused")
	tests := []struct {
		name        string
		count       int
		countErr    error
		interactive bool
		answer      string
		token       string
		wantCode    int // 0 means proceed (nil error)
		wantOut     []string
	}{
		{
			name:     "unknown count, non-interactive, no token",
			countErr: countErr,
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Existing issues: unknown (could not count them: open store: connection refused)", "bd doctor", "Refusing to destroy an unknown number of issues", "bd help init-safety"},
		},
		{
			name:     "unknown count, non-interactive, wrong token",
			countErr: countErr,
			token:    "DESTROY-other",
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Refusing to destroy an unknown number of issues"},
		},
		{
			name:     "unknown count, non-interactive, token",
			countErr: countErr,
			token:    "DESTROY-bd",
			wantOut:  []string{"Existing issues: unknown", "Destroy token accepted"},
		},
		{
			name:        "unknown count, interactive, confirmed",
			countErr:    countErr,
			interactive: true,
			answer:      "destroy all issues\n",
			wantOut:     []string{"Existing issues: unknown (could not count them: open store: connection refused)", "Type 'destroy all issues' to confirm"},
		},
		{
			name:        "unknown count, interactive, declined",
			countErr:    countErr,
			interactive: true,
			answer:      "yes\n",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Type 'destroy all issues' to confirm", "Aborted. Database was NOT modified."},
		},
		{
			name:        "unknown count, interactive, a count is not all",
			countErr:    countErr,
			interactive: true,
			answer:      "destroy 0 issues\n",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Aborted. Database was NOT modified."},
		},
		{
			name:     "known count, non-interactive, no token",
			count:    3,
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Existing issues: 3", "Refusing to destroy 3 issues in non-interactive mode."},
		},
		{
			name:    "known count, non-interactive, token",
			count:   3,
			token:   "DESTROY-bd",
			wantOut: []string{"Existing issues: 3", "Destroy token accepted"},
		},
		{
			name:        "known count, interactive, confirmed",
			count:       3,
			interactive: true,
			answer:      "destroy 3 issues\n",
			wantOut:     []string{"Type 'destroy 3 issues' to confirm"},
		},
		{
			name:        "known count, interactive, declined",
			count:       3,
			interactive: true,
			answer:      "no\n",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Aborted. Database was NOT modified."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := confirmReinitDestroy(&buf, strings.NewReader(tt.answer), tt.interactive, target, tt.count, tt.countErr, "bd", tt.token)
			out := buf.String()
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("want nil (proceed), got %v\n%s", err, out)
				}
			} else {
				requireExitCode(t, err, tt.wantCode)
			}
			if !strings.Contains(out, "Target: "+target) {
				t.Errorf("warning must name the target, got:\n%s", out)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q, got:\n%s", want, out)
				}
			}
			if strings.Contains(out, "DESTROY-") {
				t.Errorf("output must never print the destroy token value, got:\n%s", out)
			}
		})
	}

	t.Run("count error is spelled, not printed raw", func(t *testing.T) {
		var buf bytes.Buffer
		err := confirmReinitDestroy(&buf, strings.NewReader(""), false, target, 0, errors.New("bad \x1b[2Jterminal"), "bd", "")
		requireExitCode(t, err, ExitDestroyTokenMissing)
		if strings.Contains(buf.String(), "\x1b") {
			t.Errorf("count error reached the terminal raw:\n%q", buf.String())
		}
	})
}

// TestCountReinitTargetIssues pins what the reinit preflight treats as an
// uncountable workspace: one it cannot open is an error, never zero. Only
// positive evidence that there is nothing to destroy counts as zero, and a
// target without project files is settled without opening a store, so the
// count creates nothing before the confirmation.
func TestCountReinitTargetIssues(t *testing.T) {
	t.Run("missing directory counts zero and stays missing", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		count, err := countReinitTargetIssues(beadsDir)
		if err != nil || count != 0 {
			t.Fatalf("want 0, nil; got %d, %v", count, err)
		}
		if _, statErr := os.Stat(beadsDir); !os.IsNotExist(statErr) {
			t.Fatalf("counting a missing target created it: %v", statErr)
		}
	})

	for name, files := range map[string][]string{
		"empty directory":        nil,
		"only a legacy registry": {"registry.json"},
	} {
		t.Run(name+" counts zero and creates nothing", func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if err := os.WriteFile(filepath.Join(beadsDir, f), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			count, err := countReinitTargetIssues(beadsDir)
			if err != nil || count != 0 {
				t.Fatalf("want 0, nil; got %d, %v", count, err)
			}
			entries, readErr := os.ReadDir(beadsDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != len(files) {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Fatalf("counting a target without project files created entries in it: %v", names)
			}
		})
	}

	t.Run("unparseable metadata.json is an error", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeReinitTargetMetadata(t, beadsDir, []byte("{not json"))
		if _, err := countReinitTargetIssues(beadsDir); err == nil {
			t.Fatal("want an error for an unloadable metadata.json, got nil")
		}
	})

	t.Run("server mode with a local copy and no server is an error", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
		t.Setenv("BEADS_DOLT_AUTO_START", "0")
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		data, _ := json.Marshal(map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
			"project_id":    "existing-0000-1111-2222-333344445555",
		})
		writeReinitTargetMetadata(t, beadsDir, data)
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "myproject", ".dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := countReinitTargetIssues(beadsDir); err == nil {
			t.Fatal("want an error: a local copy of the database is never proof that it is absent")
		}
	})
}

// TestCountReinitTargetIssues_ServerReportsDatabaseAbsent is the one open
// failure that proves there is nothing to destroy: a server-mode workspace with
// no local copy of its database, whose reachable server reports that database
// absent. That counts zero, so --recreate-missing against a reachable server
// still needs no destroy token. Needs a Dolt binary.
func TestCountReinitTargetIssues_ServerReportsDatabaseAbsent(t *testing.T) {
	beadsDir := startProjectServerModeGuardFixture(t, "myproject")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	count, err := countReinitTargetIssues(beadsDir)
	if err != nil || count != 0 {
		t.Fatalf("want 0, nil for a database the reachable server reports absent; got %d, %v", count, err)
	}
	var out string
	out = captureStderr(t, func() {
		err = runInitReinitPreflight(true, beadsDir, "myproject", "")
	})
	if err != nil {
		t.Fatalf("preflight must pass with nothing to destroy, got %v\n%s", err, out)
	}
	if strings.Contains(out, "WARNING") {
		t.Errorf("preflight warned with nothing to destroy:\n%s", out)
	}
}

// TestInitReinitLocal_CountsTheGateTargetFromLinkedWorktree pins GH#5628
// part 2 through the init command itself. From a linked worktree, init locks
// and replaces the main checkout's .beads (GetWorktreeFallbackBeadsDir), but
// discovery prefers the worktree's own .beads when it holds a database. The
// preflight used to count the latter, so an empty worktree database read as
// "nothing to destroy" while the main checkout's issues were replaced.
func TestInitReinitLocal_CountsTheGateTargetFromLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitAt(t, mainDir)
	for _, args := range [][]string{
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init"},
		{"worktree", "add", filepath.Join(root, "wt")},
	} {
		if out, err := initArtifactGitCommand(mainDir, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	metadata := []byte(`{"database":"dolt","backend":"dolt"}`)
	mainBeads := filepath.Join(mainDir, ".beads")
	writeReinitTargetMetadata(t, mainBeads, metadata)
	// The worktree's own .beads holds a database of its own, so discovery
	// picks it.
	wtBeads := filepath.Join(root, "wt", ".beads")
	writeReinitTargetMetadata(t, wtBeads, metadata)
	if err := os.MkdirAll(filepath.Join(wtBeads, "embeddeddolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(filepath.Join(root, "wt"))
	git.ResetCaches()
	beads.ResetCaches()
	t.Cleanup(func() {
		git.ResetCaches()
		beads.ResetCaches()
	})
	stubCallerBeadsDir(t, "")
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BD_NON_INTERACTIVE", "1")
	setInitFlag(t, "reinit-local", "true")
	setInitFlag(t, "prefix", "wt")
	setInitFlag(t, "skip-hooks", "true")
	oldCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = oldCtx })

	wantTarget := utils.CanonicalizePath(mainBeads)
	origCount := countReinitTarget
	countReinitTarget = func(beadsDir string) (int, error) {
		if utils.CanonicalizePath(beadsDir) == wantTarget {
			return 5, nil
		}
		t.Errorf("preflight counted %s, not the .beads init replaces (%s)", beadsDir, wantTarget)
		// An error, not 0, so a wrong count still stops at the refusal
		// instead of re-initializing the test fixture.
		return 0, errors.New("counted the wrong workspace")
	}
	t.Cleanup(func() { countReinitTarget = origCount })

	var err error
	out := captureStderr(t, func() {
		err = initCmd.RunE(initCmd, nil)
	})
	requireExitCode(t, err, ExitDestroyTokenMissing)
	if !strings.Contains(out, "Target: "+wantTarget) {
		t.Errorf("warning must name the main checkout's .beads %s, got:\n%s", wantTarget, out)
	}
	if !strings.Contains(out, "Existing issues: 5") {
		t.Errorf("warning must show the main checkout's issue count, got:\n%s", out)
	}
	if got, readErr := os.ReadFile(filepath.Join(mainBeads, "metadata.json")); readErr != nil || !bytes.Equal(got, metadata) {
		t.Errorf("the main checkout's .beads must survive the refusal: %v %q", readErr, got)
	}
}

// TestRunInitReinitPreflight_WorktreeCountsMainCheckout pins the divergence
// behind GH#5628 part 2 without a Dolt server: in a linked worktree whose own
// .beads holds an empty database, FindBeadsDir (what the old count resolved)
// picks the worktree's .beads, while init's gate target is the main checkout's
// (GetWorktreeFallbackBeadsDir). Counting the gate target must refuse on the
// main checkout's uncountable database, name it, and leave it unchanged.
//
// Deliberately not t.Parallel(): t.Chdir and a shared git worktree fixture.
func TestRunInitReinitPreflight_WorktreeCountsMainCheckout(t *testing.T) {
	isolateBeadsDirForTest(t)

	root := t.TempDir()
	mainRepo := filepath.Join(root, "main-repo")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitAt(t, mainRepo)
	worktreeDir := filepath.Join(root, "worktree")
	for _, args := range [][]string{
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init"},
		{"worktree", "add", worktreeDir},
	} {
		if out, err := initArtifactGitCommand(mainRepo, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// The main checkout's .beads: beads project files, uncountable.
	mainBeadsDir := filepath.Join(mainRepo, ".beads")
	writeReinitTargetMetadata(t, mainBeadsDir, []byte("{not json"))
	metadataPath := filepath.Join(mainBeadsDir, "metadata.json")
	beforeMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := countReinitTargetIssues(mainBeadsDir); err == nil {
		t.Fatal("precondition: the main checkout's .beads must be uncountable")
	}
	// The worktree's own .beads: project files and an empty database of its
	// own, so discovery prefers it over the main checkout.
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	writeReinitTargetMetadata(t, worktreeBeadsDir, []byte(`{"backend": "dolt", "database": "dolt"}`))
	if err := os.MkdirAll(filepath.Join(worktreeBeadsDir, "dolt", "emptydb", ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()
	beads.ResetCaches()
	t.Cleanup(func() {
		git.ResetCaches()
		beads.ResetCaches()
	})
	if !git.IsWorktree() {
		t.Fatal("precondition: expected git.IsWorktree() inside the linked worktree")
	}
	if got, want := utils.CanonicalizePath(beads.FindBeadsDir()), utils.CanonicalizePath(worktreeBeadsDir); got != want {
		t.Fatalf("precondition: FindBeadsDir = %q, want the worktree's own %q", got, want)
	}
	gateTarget := beads.GetWorktreeFallbackBeadsDir()
	if got, want := utils.CanonicalizePath(gateTarget), utils.CanonicalizePath(mainBeadsDir); got != want {
		t.Fatalf("precondition: the gate target %q is not the main checkout's .beads %q", got, want)
	}

	var refusal error
	out := captureStderr(t, func() {
		refusal = runInitReinitPreflight(true, gateTarget, "bd", "")
	})
	requireExitCode(t, refusal, ExitDestroyTokenMissing)
	if !strings.Contains(out, "Target: "+gateTarget) {
		t.Errorf("warning must name the main checkout's .beads as the target, got:\n%s", out)
	}
	if !strings.Contains(out, "Refusing to destroy an unknown number of issues") {
		t.Errorf("warning must report the unknown count, got:\n%s", out)
	}
	if got, err := os.ReadFile(metadataPath); err != nil || !bytes.Equal(got, beforeMetadata) {
		t.Errorf("the refusal must leave the main checkout's .beads unchanged, got %q, %v", got, err)
	}
	entries, err := os.ReadDir(mainBeadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "metadata.json" {
		t.Errorf("the refusal must create nothing in the main checkout's .beads, found %d entries", len(entries))
	}
}
