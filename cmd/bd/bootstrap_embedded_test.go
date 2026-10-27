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

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
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

// GH#3494: a workspace whose .beads/config.yaml carries issue-prefix (what
// gc init / gc rig add write) must come out of `bd bootstrap` with that
// prefix in the database scope, so `bd config get` and `bd create` see it.
func TestEmbeddedBootstrapPersistsConfigYamlPrefix(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "issue_prefix: la\nissue-prefix: la\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	out := bdBootstrap(t, bd, dir, "--yes")
	t.Logf("bootstrap output:\n%s", out)

	get := exec.Command(bd, "config", "get", "issue_prefix")
	get.Dir = dir
	get.Env = bdEnv(dir)
	got, err := get.Output()
	if err != nil {
		t.Fatalf("bd config get issue_prefix: %v\n%s", err, got)
	}
	if strings.TrimSpace(string(got)) != "la" {
		t.Errorf("after bootstrap, bd config get issue_prefix = %q, want %q (config.yaml prefix)", strings.TrimSpace(string(got)), "la")
	}

	create := exec.Command(bd, "create", "--type", "task", "--title", "x", "--silent")
	create.Dir = dir
	create.Env = bdEnv(dir)
	id, err := create.Output()
	if err != nil {
		t.Fatalf("bd create after bootstrap: %v\n%s", err, id)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(id)), "la-") {
		t.Errorf("bd create after bootstrap made %q, want an la- id", strings.TrimSpace(string(id)))
	}
}

// embeddedWorkspaceMissingPrefix initializes an embedded workspace whose
// database is named database, then deletes its issue_prefix row: the state a
// setup interrupted between creating the database and seeding its prefix
// leaves. yamlPrefix, when set, becomes config.yaml's issue-prefix; otherwise
// config.yaml names none.
func embeddedWorkspaceMissingPrefix(t *testing.T, bd, database, yamlPrefix string) (dir, beadsDir string) {
	t.Helper()
	dir, beadsDir, _ = bdInit(t, bd, "--prefix", database)

	s, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open embedded store: %v", err)
	}
	if err := s.DeleteConfig(t.Context(), "issue_prefix"); err != nil {
		_ = s.Close()
		t.Fatalf("delete issue_prefix: %v", err)
	}
	if err := s.Commit(t.Context(), "test: drop issue_prefix"); err != nil {
		_ = s.Close()
		t.Fatalf("commit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close embedded store: %v", err)
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, "# "), "issue-prefix:") || strings.HasPrefix(strings.TrimLeft(line, "# "), "issue_prefix:") {
			continue
		}
		lines = append(lines, line)
	}
	if yamlPrefix != "" {
		lines = append(lines, fmt.Sprintf("issue-prefix: %q", yamlPrefix))
	}
	if err := os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return dir, beadsDir
}

// bdConfigGetPrefix returns `bd config get issue_prefix` stdout, trimmed.
func bdConfigGetPrefix(t *testing.T, bd, dir string) string {
	t.Helper()
	cmd := exec.Command(bd, "config", "get", "issue_prefix")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd config get issue_prefix: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// bdCreateID runs `bd create --silent` and returns the new issue's ID.
func bdCreateID(t *testing.T, bd, dir string) string {
	t.Helper()
	cmd := exec.Command(bd, "create", "--type", "task", "--title", "x", "--silent")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd create: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// An embedded database that exists but has no issue_prefix is finished by
// `bd bootstrap`: the prefix comes from config.yaml issue-prefix, else the
// database name, where bootstrap used to report "Database already exists" and
// leave every `bd create` failing.
func TestEmbeddedBootstrapSeedsMissingPrefix(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	for _, tc := range []struct{ name, yamlPrefix, want string }{
		{"from_config_yaml", "la", "la"},
		{"from_database_name", "", "zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := embeddedWorkspaceMissingPrefix(t, bd, "zz", tc.yamlPrefix)

			cmd := exec.Command(bd, "bootstrap", "--yes")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			if err != nil {
				t.Fatalf("bd bootstrap --yes: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "already exists") {
				t.Errorf("bootstrap reported an existing database instead of seeding its prefix:\n%s", stdout.String())
			}

			if got := bdConfigGetPrefix(t, bd, dir); got != tc.want {
				t.Errorf("after bootstrap, bd config get issue_prefix = %q, want %q\nbootstrap stdout:\n%s\nstderr:\n%s", got, tc.want, stdout.String(), stderr.String())
			}
			if id := bdCreateID(t, bd, dir); !strings.HasPrefix(id, tc.want+"-") {
				t.Errorf("bd create after bootstrap made %q, want a %s- id", id, tc.want)
			}
		})
	}
}

// AC-4: a seed-prefix plan made while the database had no issue_prefix
// leaves alone a prefix written before it executes, and says so.
func TestEmbeddedBootstrapSeedPrefixKeepsPrefixSetAfterPlanning(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir := embeddedWorkspaceMissingPrefix(t, bd, "zz", "la")
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	plan := BootstrapPlan{Action: "seed-prefix", BeadsDir: beadsDir, Database: cfg.GetDoltDatabase(), HasExisting: true}

	// Another process sets the prefix between planning and execution.
	s, err := embeddeddolt.Open(t.Context(), beadsDir, "zz", "main")
	if err != nil {
		t.Fatalf("open embedded store: %v", err)
	}
	if err := s.SetConfig(t.Context(), "issue_prefix", "xx"); err != nil {
		_ = s.Close()
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := s.Commit(t.Context(), "test: set issue_prefix"); err != nil {
		_ = s.Close()
		t.Fatalf("commit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var execErr error
	stderr := captureStderr(t, func() {
		execErr = executeSeedPrefixAction(t.Context(), plan, cfg)
	})
	if execErr != nil {
		t.Fatalf("executeSeedPrefixAction: %v\nstderr:\n%s", execErr, stderr)
	}
	if want := `issue_prefix already set to "xx"; left unchanged`; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
	if got := bdConfigGetPrefix(t, bd, dir); got != "xx" {
		t.Errorf("bd config get issue_prefix = %q, want %q", got, "xx")
	}
}

// AC-5: the dry-run plan for an embedded database missing its prefix is a
// non-blocked seed-prefix on an existing database whose reason names the
// prefix and where it came from, and the real run reports the same.
func TestEmbeddedBootstrapSeedPrefixPlanAndMessage(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _ := embeddedWorkspaceMissingPrefix(t, bd, "la", "la")

	cmd := exec.Command(bd, "bootstrap", "--dry-run", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd bootstrap --dry-run --json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var plan map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("parse plan JSON: %v\n%s", err, stdout.String())
	}
	if plan["action"] != "seed-prefix" {
		t.Errorf("action = %v, want seed-prefix\n%s", plan["action"], stdout.String())
	}
	if plan["has_existing"] != true {
		t.Errorf("has_existing = %v, want true", plan["has_existing"])
	}
	if _, ok := plan["blocked"]; ok {
		t.Errorf("plan carries blocked: %v", plan["blocked"])
	}
	if reason, _ := plan["reason"].(string); !strings.Contains(reason, `"la"`) || !strings.Contains(reason, "config.yaml issue-prefix") {
		t.Errorf("reason %q does not name the prefix and its source", reason)
	}

	cmd = exec.Command(bd, "bootstrap", "--yes")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err = runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd bootstrap --yes: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if want := `Set issue_prefix "la" on existing database "la" (from config.yaml issue-prefix)`; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "Created fresh database") {
		t.Errorf("bootstrap reported a fresh database:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

// AC-6: with config.yaml issue-prefix set and the database's issue_prefix
// empty, `bd config get issue_prefix` and `bd config show` keep stdout as it
// was and add one stderr note naming bd bootstrap; --json is untouched.
func TestEmbeddedConfigNotesYAMLPrefixIsNotDatabasePrefix(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _ := embeddedWorkspaceMissingPrefix(t, bd, "la", "la")
	const note = `config.yaml issue-prefix "la" is not the database prefix; run 'bd bootstrap' to set it`

	run := func(args ...string) (string, string) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	stdout, stderr := run("config", "get", "issue_prefix")
	if stdout != "issue_prefix (not set)\n" {
		t.Errorf("config get stdout = %q, want %q", stdout, "issue_prefix (not set)\n")
	}
	if n := strings.Count(stderr, note); n != 1 {
		t.Errorf("config get stderr carries the note %d times, want 1:\n%s", n, stderr)
	}

	stdout, stderr = run("config", "get", "issue_prefix", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse config get --json: %v\n%s", err, stdout)
	}
	if got["key"] != "issue_prefix" || got["value"] != "" {
		t.Errorf("config get --json = %v, want key issue_prefix with an empty value", got)
	}
	if strings.Contains(stdout+stderr, "is not the database prefix") {
		t.Errorf("config get --json carries the note:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	stdout, stderr = run("config", "show")
	if strings.Contains(stdout, "is not the database prefix") {
		t.Errorf("config show printed the note on stdout:\n%s", stdout)
	}
	if n := strings.Count(stderr, note); n != 1 {
		t.Errorf("config show stderr carries the note %d times, want 1:\n%s", n, stderr)
	}

	stdout, stderr = run("config", "show", "--json")
	if !json.Valid([]byte(stdout)) {
		t.Errorf("config show --json is not JSON:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, "is not the database prefix") {
		t.Errorf("config show --json carries the note:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	// Once the database holds its prefix there is nothing to note.
	run("bootstrap", "--yes")
	for _, args := range [][]string{{"config", "get", "issue_prefix"}, {"config", "show"}} {
		if _, stderr := run(args...); strings.Contains(stderr, "is not the database prefix") {
			t.Errorf("bd %s still notes after bootstrap:\n%s", strings.Join(args, " "), stderr)
		}
	}
}
