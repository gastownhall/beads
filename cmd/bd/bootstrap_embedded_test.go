//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// bdBootstrap runs "bd bootstrap" with the given args and returns stdout.
func bdBootstrap(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"bootstrap"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd bootstrap %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func bdBootstrapAllowError(t *testing.T, bd, dir string, args ...string) (string, error) {
	t.Helper()
	fullArgs := append([]string{"bootstrap"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestBootstrapNoWorkspace(t *testing.T) {
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()
	initGitRepoAt(t, dir)

	t.Run("default_output", func(t *testing.T) {
		out, err := bdBootstrapAllowError(t, bd, dir)
		if err == nil {
			t.Fatal("expected bd bootstrap to exit non-zero without a workspace")
		}
		if !strings.Contains(out, activeWorkspaceNotFoundMessage()) {
			t.Fatalf("expected no-workspace message, got: %s", out)
		}
		if !strings.Contains(out, "bd where") {
			t.Fatalf("expected bootstrap hint to mention bd where, got: %s", out)
		}
	})

	t.Run("json_output", func(t *testing.T) {
		cmd := exec.Command(bd, "bootstrap", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err == nil {
			t.Fatal("expected bd bootstrap --json to exit non-zero without a workspace")
		}

		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("expected JSON object in output:\nstdout: %s\nstderr: %s", s, stderr.String())
		}

		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &payload); err != nil {
			t.Fatalf("parse bootstrap JSON: %v\nstdout: %s", err, s)
		}
		if action, _ := payload["action"].(string); action != "none" {
			t.Fatalf("action = %q, want %q", action, "none")
		}
		if reason, _ := payload["reason"].(string); reason != activeWorkspaceNotFoundError() {
			t.Fatalf("reason = %q, want %q", reason, activeWorkspaceNotFoundError())
		}
		if suggestion, _ := payload["suggestion"].(string); !strings.Contains(suggestion, "bd where") {
			t.Fatalf("suggestion should mention bd where, got: %q", suggestion)
		}
	})
}

func TestEmbeddedBootstrapExistingDryRunInit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Already Exists =====

	t.Run("bootstrap_existing_db", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "tb")
		out := bdBootstrap(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "already exists") && !strings.Contains(out, "Nothing to do") {
			t.Errorf("expected 'already exists' for initialized db: %s", out)
		}
	})

	// ===== Dry Run (fresh .beads with no db) =====

	t.Run("bootstrap_dry_run_fresh", func(t *testing.T) {
		dir := t.TempDir()
		cmd := gitCommand("init", "-q")
		cmd.Dir = dir
		cmd.CombinedOutput()
		cmd = gitCommand("config", "user.name", "Test")
		cmd.Dir = dir
		cmd.CombinedOutput()
		cmd = gitCommand("config", "user.email", "test@test.com")
		cmd.Dir = dir
		cmd.CombinedOutput()
		// Create .beads with metadata.json so FindBeadsDir detects it
		beadsDir := filepath.Join(dir, ".beads")
		os.MkdirAll(beadsDir, 0o750)
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0o644)

		out := bdBootstrap(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "create fresh") && !strings.Contains(out, "init") {
			t.Errorf("expected fresh init plan: %s", out)
		}
	})

	// ===== Full Bootstrap (init action) =====

	t.Run("bootstrap_init", func(t *testing.T) {
		dir := t.TempDir()
		cmd := gitCommand("init", "-q")
		cmd.Dir = dir
		cmd.CombinedOutput()
		cmd = gitCommand("config", "user.name", "Test")
		cmd.Dir = dir
		cmd.CombinedOutput()
		cmd = gitCommand("config", "user.email", "test@test.com")
		cmd.Dir = dir
		cmd.CombinedOutput()
		beadsDir := filepath.Join(dir, ".beads")
		os.MkdirAll(beadsDir, 0o750)
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0o644)

		bcmd := exec.Command(bd, "bootstrap")
		bcmd.Dir = dir
		bcmd.Env = bdEnv(dir)
		bcmd.Stdin = strings.NewReader("y\n")
		stdout, stderr, err := runCommandBuffers(t, bcmd)
		if err != nil {
			t.Fatalf("bootstrap init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "Created fresh database") {
			t.Errorf("expected 'Created fresh database':\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	})
}

func TestEmbeddedBootstrapJSONLAndGitOrigin(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== JSONL Import =====

	t.Run("bootstrap_jsonl_import", func(t *testing.T) {
		// First create a db and export
		srcDir, _, _ := bdInit(t, bd, "--prefix", "bs")
		bdCreate(t, bd, srcDir, "Export for bootstrap", "--type", "task")
		cmd := exec.Command(bd, "export", "-o", filepath.Join(srcDir, ".beads", "issues.jsonl"))
		cmd.Dir = srcDir
		cmd.Env = bdEnv(srcDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("export failed: %v\n%s", err, out)
		}

		// Create new dir with .beads + issues.jsonl but no database
		dir := t.TempDir()
		gitCmd := gitCommand("init", "-q")
		gitCmd.Dir = dir
		gitCmd.CombinedOutput()
		gitCmd = gitCommand("config", "user.name", "Test")
		gitCmd.Dir = dir
		gitCmd.CombinedOutput()
		gitCmd = gitCommand("config", "user.email", "test@test.com")
		gitCmd.Dir = dir
		gitCmd.CombinedOutput()
		destBeads := filepath.Join(dir, ".beads")
		os.MkdirAll(destBeads, 0o750)
		os.WriteFile(filepath.Join(destBeads, "metadata.json"), []byte("{}"), 0o644)

		// Copy the JSONL file
		data, _ := os.ReadFile(filepath.Join(srcDir, ".beads", "issues.jsonl"))
		os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), data, 0o644)

		// Bootstrap should detect and import JSONL
		bcmd := exec.Command(bd, "bootstrap")
		bcmd.Dir = dir
		bcmd.Env = bdEnv(dir)
		bcmd.Stdin = strings.NewReader("y\n")
		stdout, stderr, err := runCommandBuffers(t, bcmd)
		if err != nil {
			t.Fatalf("bootstrap jsonl-import failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "Imported") {
			t.Errorf("expected 'Imported' in output:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	})

	t.Run("bootstrap_from_git_origin_wires_remote", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "origin.git")
		runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", bareDir)
		remoteURL := "file://" + bareDir

		sourceDir := t.TempDir()
		initGitRepoAt(t, sourceDir)
		runGitForBootstrapTest(t, sourceDir, "branch", "-M", "main")
		runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", remoteURL)
		runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
		runGitForBootstrapTest(t, sourceDir, "push", "-u", "origin", "main")
		runBDInit(t, bd, sourceDir, "--prefix", "beads", "--skip-hooks", "--skip-agents")
		bdCreate(t, bd, sourceDir, "Seed remote data", "--type", "task")
		bdDolt(t, bd, sourceDir, "push")

		cloneDir := t.TempDir()
		runGitForBootstrapTest(t, cloneDir, "init", "-b", "main")
		runGitForBootstrapTest(t, cloneDir, "remote", "add", "origin", remoteURL)

		out := bdBootstrap(t, bd, cloneDir, "--yes")
		if !strings.Contains(out, "clone from remote") {
			t.Fatalf("expected bootstrap sync plan, got:\n%s", out)
		}

		remotes := bdDolt(t, bd, cloneDir, "remote", "list")
		if !strings.Contains(remotes, "origin") || !strings.Contains(remotes, remoteURL) {
			t.Fatalf("bootstrap should leave origin configured as a Dolt remote %q; remote list:\n%s", remoteURL, remotes)
		}
		configYAML, err := os.ReadFile(filepath.Join(cloneDir, ".beads", "config.yaml"))
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		if !strings.Contains(string(configYAML), remoteURL) {
			t.Fatalf("bootstrap should persist sync.remote; config.yaml:\n%s", configYAML)
		}
	})
}

// TestEmbeddedAdoptRecipeRunsOnAGatedClone runs the gate's adopt recipe on a
// clone the gate refuses with its adopt decision (bd-bt60zz, #6687 review):
// the remote holds this binary's schema, the clone's recorded cursor is one
// migration behind, and sync.remote is unset, so the remote is recorded only
// in the database the recipe moves aside, the case its remote check exists
// for. The schema package's tests pin the recipe's text and cannot see
// whether a step runs on such a clone: an earlier recipe set sync.remote with
// `bd config set`, which the gate refuses like any write. Here each step
// before the move exits 0 and takes effect, the move and bootstrap run as
// schema.EmbeddedAdoptCommands lists them, and the clone ends on the remote's
// schema and data with the moved-aside copy kept.
func TestEmbeddedAdoptRecipeRunsOnAGatedClone(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "adp")
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// The remote gets this binary's schema and an issue. `bd dolt remote add
	// origin` also records the remote in sync.remote; the unset leaves it
	// recorded only in the database.
	pushed := bdCreate(t, bd, dir, "Pushed before the adopt", "--type", "task")
	remoteURL := "file://" + filepath.ToSlash(t.TempDir())
	bdDolt(t, bd, dir, "remote", "add", "origin", remoteURL)
	bdDolt(t, bd, dir, "push")
	if out, err := run("config", "unset", "sync.remote"); err != nil {
		t.Fatalf("bd config unset sync.remote: %v\n%s", err, out)
	}
	regressEmbeddedSchemaCursor(t, beadsDir, "adp")

	// A write is refused with the adopt decision, and the refusal names the
	// steps this test runs.
	refusal, err := run("create", "Refused on the gated clone")
	if err == nil || !strings.Contains(refusal, "the remote is already migrated") {
		t.Fatalf("bd create on the gated clone = %v, want the adopt refusal:\n%s", err, refusal)
	}
	text := strings.Join(strings.Fields(refusal), " ")
	for _, want := range []string{
		"check that `bd config get sync.remote` prints the remote to adopt (`bd dolt remote list` shows this clone's remotes)",
		"set `remote: <url>` under `sync:` in the config.yaml of the .beads that `bd where` reports, by hand: `bd config set` is refused while this database has pending migrations",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the adopt refusal lacks %q:\n%s", want, refusal)
		}
	}

	// The check runs on the gated clone: `bd config get` of a config.yaml key
	// skips the store open, and `bd dolt remote list` opens it leniently.
	if out, err := run("config", "get", "sync.remote"); err != nil || strings.Contains(out, remoteURL) {
		t.Fatalf("bd config get sync.remote = %v, want exit 0 and no remote set:\n%s", err, out)
	}
	if out, err := run("dolt", "remote", "list"); err != nil || !strings.Contains(out, remoteURL) {
		t.Fatalf("bd dolt remote list = %v, want exit 0 and %s listed:\n%s", err, remoteURL, out)
	}

	// `bd config set` is refused and writes nothing, so the recipe has the
	// operator edit config.yaml: remote: under sync:, where the unset above
	// left the remote commented out.
	configPath := filepath.Join(beadsDir, "config.yaml")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run("config", "set", "sync.remote", remoteURL); err == nil || !strings.Contains(out, "the remote is already migrated") {
		t.Fatalf("bd config set sync.remote on the gated clone = %v, want the adopt refusal:\n%s", err, out)
	}
	if after, err := os.ReadFile(configPath); err != nil || string(after) != string(before) {
		t.Fatalf("the refused bd config set changed config.yaml (err %v):\n%s", err, after)
	}
	edited := strings.Replace(string(before), "\nsync:\n", "\nsync:\n  remote: \""+remoteURL+"\"\n", 1)
	if edited == string(before) {
		t.Fatalf("config.yaml has no sync: section to set remote: under:\n%s", before)
	}
	if err := os.WriteFile(configPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run("config", "get", "sync.remote"); err != nil || !strings.Contains(out, remoteURL) {
		t.Fatalf("bd config get sync.remote after the edit = %v, want %s:\n%s", err, remoteURL, out)
	}

	// The move and bootstrap as the gate lists them, with the plan checked
	// before bootstrap acts: only a clone from that remote is the adopt.
	for _, step := range schema.EmbeddedAdoptCommands() {
		args := strings.Fields(step)
		if args[0] == "bd" {
			plan, err := run("bootstrap", "--dry-run")
			if err != nil || !strings.Contains(plan, "Bootstrap plan: clone from remote") || !strings.Contains(plan, "Remote: "+remoteURL) {
				t.Fatalf("bd bootstrap --dry-run after the move = %v, want a clone from %s:\n%s", err, remoteURL, plan)
			}
			args[0] = bd
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("recipe step %q: %v\n%s", step, err, out)
		}
	}

	if _, err := os.Stat(filepath.Join(beadsDir, "backup", "embeddeddolt.pre-adopt")); err != nil {
		t.Errorf("the moved-aside copy is gone after the adopt: %v", err)
	}
	if got := readEmbeddedStoreSnapshot(t, beadsDir, "adp").schemaVersion; got != schema.LatestVersion() {
		t.Errorf("schema version after the adopt = %d, want the remote's %d", got, schema.LatestVersion())
	}
	if out, err := run("show", pushed.ID); err != nil {
		t.Errorf("bd show %s (the pushed issue) after the adopt: %v\n%s", pushed.ID, err, out)
	}
	if out, err := run("create", "Written after the adopt"); err != nil {
		t.Errorf("bd create after the adopt: %v\n%s", err, out)
	}
}

// TestEmbeddedBootstrapConcurrent exercises bootstrap --dry-run concurrently.
func TestEmbeddedBootstrapConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "bx")

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			cmd := exec.Command(bd, "bootstrap", "--dry-run")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("bootstrap --dry-run (worker %d): %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}

// TestEmbeddedBootstrapCloneFailureScrubsCredentials pins the credential scrub
// at the "Bootstrap failed:" print site (mc-gcmer). An unencoded "#" or "?" in
// the password ends the URL's authority early, so the remote is not a forge
// and is cloned as configured. The clone error then carries the password
// twice: sanitizeURL passes a URL net/url cannot parse through unchanged, and
// Dolt's "invalid remote url:" repeats it.
func TestEmbeddedBootstrapCloneFailureScrubsCredentials(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	for _, tc := range []struct{ name, password string }{
		{"hash", "pa#ss"},
		{"question_mark", "pa?ss"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepoAt(t, dir)
			beadsDir := filepath.Join(dir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatalf("mkdir .beads: %v", err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0o644); err != nil {
				t.Fatalf("write metadata.json: %v", err)
			}
			seedSyncRemote(t, beadsDir, "https://alice:"+tc.password+"@github.com/org/db")

			cmd := exec.Command(bd, "bootstrap", "--yes")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			// A failure before the clone would pass the leak check below
			// without reaching the print site.
			if err == nil || !strings.Contains(stderr.String(), "Bootstrap failed: clone from remote:") {
				t.Fatalf("bootstrap should fail at the clone, got err=%v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), tc.password) {
				t.Errorf("bootstrap printed the sync.remote password %q:\nstdout:\n%s\nstderr:\n%s", tc.password, stdout.String(), stderr.String())
			}
		})
	}
}
