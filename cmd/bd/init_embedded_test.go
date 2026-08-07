//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

var (
	embeddedBDOnce sync.Once
	embeddedBD     string
	embeddedBDErr  error
)

// buildEmbeddedBD returns the path to an embedded bd binary for subprocess tests.
// If BEADS_TEST_BD_BINARY is set, uses that pre-built binary (skipping the ~45s build).
// CI can pre-build once and pass the path to all test invocations.
func buildEmbeddedBD(t *testing.T) string {
	t.Helper()
	embeddedBDOnce.Do(func() {
		if prebuilt := os.Getenv("BEADS_TEST_BD_BINARY"); prebuilt != "" {
			if _, err := os.Stat(prebuilt); err != nil {
				embeddedBDErr = fmt.Errorf("BEADS_TEST_BD_BINARY=%q not found: %w", prebuilt, err)
				return
			}
			embeddedBD = prebuilt
			return
		}
		tmpDir, err := testTempDir("bd-embedded-init-test-*")
		if err != nil {
			embeddedBDErr = fmt.Errorf("failed to create temp dir: %w", err)
			return
		}
		name := "bd"
		if runtime.GOOS == "windows" {
			name = "bd.exe"
		}
		embeddedBD = filepath.Join(tmpDir, name)
		cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", embeddedBD, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			embeddedBDErr = fmt.Errorf("go build failed: %v\n%s", err, out)
		}
	})
	if embeddedBDErr != nil {
		t.Fatalf("Failed to build embedded bd binary: %v", embeddedBDErr)
	}
	return embeddedBD
}

func initGitRepoAt(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		// Force repo-local hooks so tests ignore any global hooksPath override.
		{"config", "core.hooksPath", ".git/hooks"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", args[0], err, out)
		}
	}
}

func bdEnv(dir string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BEADS_") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"HOME="+dir,
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_NO_DAEMON=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
	)
}

// envWithout returns env minus any entries for the named variable.
func envWithout(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// isEmbeddedLockOutput recognizes every "another process holds the lock"
// outcome for concurrent bd commands against the same embedded workspace:
// the embedded Dolt flock's own messages, and the workspacegate EXCLUSIVE
// contention message (workspacegate.ErrBusy = "workspace gate busy"). The
// gate is acquired BEFORE the embedded flock is ever attempted, so a losing
// concurrent `bd init` today reports gate contention rather than a flock
// error — both are the same class of outcome from the caller's point of
// view: another bd process holds the lock, retry later.
func isEmbeddedLockOutput(out string) bool {
	out = strings.ToLower(out)
	return strings.Contains(out, "one writer at a time") ||
		strings.Contains(out, "database is locked") ||
		strings.Contains(out, "locked by another dolt process") ||
		strings.Contains(out, "workspace gate busy")
}

func runCommandBuffers(t *testing.T, cmd *exec.Cmd) (stdout, stderr bytes.Buffer, err error) {
	t.Helper()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout, stderr, err
}

// bdRunWithFlockRetry runs a bd command with retry on flock contention.
// Returns stdout and nil on success, or combined stdout/stderr and the last
// error after retries are exhausted or a non-flock error occurs.
func bdRunWithFlockRetry(t *testing.T, bd, dir string, args ...string) ([]byte, error) {
	t.Helper()
	var out []byte
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
		if err == nil {
			return stdout.Bytes(), nil
		}
		out = append(stdout.Bytes(), stderr.Bytes()...)
		if !isEmbeddedLockOutput(string(out)) {
			return out, err
		}
		t.Logf("bd %s: flock contention (attempt %d/10), retrying...", args[0], attempt+1)
		time.Sleep(time.Duration(500*(1<<min(attempt, 4))) * time.Millisecond)
	}
	return out, err
}

// bdInit creates a temp dir with a git repo, runs bd init --quiet with the
// given extra args, and returns (dir, beadsDir, combined output).
// Fatals if bd init fails.
func bdInit(t *testing.T, bd string, extraArgs ...string) (dir, beadsDir string, out string) {
	t.Helper()
	dir = t.TempDir()
	initGitRepoAt(t, dir)
	out = runBDInit(t, bd, dir, extraArgs...)
	beadsDir = filepath.Join(dir, ".beads")
	return
}

// bdInitInDir runs bd init --quiet in an existing dir. Fatals on failure.
func runBDInit(t *testing.T, bd, dir string, extraArgs ...string) string {
	t.Helper()
	args := append([]string{"init", "--quiet"}, extraArgs...)
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd init %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(extraArgs, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdInitFail runs bd init --quiet expecting failure. Returns combined output.
func bdInitFail(t *testing.T, bd string, extraArgs ...string) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	args := append([]string{"init", "--quiet"}, extraArgs...)
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("bd init should have failed")
	}
	return string(out)
}

func readBack(t *testing.T, beadsDir, database, key string, metadata bool) string {
	t.Helper()

	// The embedded dolt driver holds a process-level lock, so concurrent
	// test functions in the same shard can transiently block each other.
	// Retry a few times before giving up.
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		val, err := readBackOnce(t, beadsDir, database, key, metadata)
		if err == nil {
			return val
		}
		lastErr = err
		if !strings.Contains(err.Error(), "locked") {
			break // non-lock error, don't retry
		}
		t.Logf("readBack: attempt %d/%d got lock error, retrying: %v", attempt+1, maxAttempts, err)
	}
	t.Fatalf("readBack: %v", lastErr)
	return "" // unreachable
}

func readBackOnce(t *testing.T, beadsDir, database, key string, metadata bool) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		return "", fmt.Errorf("New failed: %w", err)
	}
	defer store.Close()
	if metadata {
		val, err := store.GetMetadata(ctx, key)
		if err != nil {
			return "", fmt.Errorf("GetMetadata(%q) failed: %w", key, err)
		}
		return val, nil
	}
	val, err := store.GetConfig(ctx, key)
	if err != nil {
		return "", fmt.Errorf("GetConfig(%q) failed: %w", key, err)
	}
	return val, nil
}

func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' && i+1 < len(s) && s[i+1] == '[' {
			for i += 2; i < len(s); i++ {
				if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') {
					break
				}
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func runDolt(t *testing.T, doltBin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(doltBin, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("dolt %s failed: %v", strings.Join(args, " "), err)
	}
	return stripANSI(string(out))
}

func doltHeadHash(t *testing.T, doltBin, dir string) string {
	t.Helper()
	line := strings.TrimSpace(runDolt(t, doltBin, dir, "log", "-n", "1", "--oneline"))
	if idx := strings.IndexByte(line, ' '); idx > 0 {
		return line[:idx]
	}
	t.Fatalf("unexpected dolt log --oneline output: %q", line)
	return ""
}

func requireFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("expected file to exist: %s", path)
	}
}

func requireNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected file not to exist: %s", path)
	}
}

func TestEmbeddedInit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("basic", func(t *testing.T) {
		dir, beadsDir, out := bdInit(t, bd, "--prefix", "basic")
		embeddedDir := filepath.Join(beadsDir, "embeddeddolt")
		requireFile(t, beadsDir)
		requireFile(t, embeddedDir)
		requireFile(t, filepath.Join(embeddedDir, "basic", ".dolt"))

		if doltBin, err := exec.LookPath("dolt"); err == nil {
			dbDir := filepath.Join(embeddedDir, "basic")
			statusOut := runDolt(t, doltBin, dbDir, "status")
			if !strings.Contains(statusOut, "nothing to commit") {
				t.Errorf("expected clean working set, got:\n%s", statusOut)
			}
			logOut := runDolt(t, doltBin, dbDir, "log", "--oneline")
			for _, want := range []string{"schema: apply migrations", "bd init"} {
				if !strings.Contains(logOut, want) {
					t.Errorf("dolt log missing %q commit:\n%s", want, logOut)
				}
			}
		}

		if val := readBack(t, beadsDir, "basic", "issue_prefix", false); val != "basic" {
			t.Errorf("issue_prefix: got %q, want %q", val, "basic")
		}
		if strings.Contains(out, "bd initialized") {
			t.Error("--quiet should suppress success message")
		}

		// bd_version is in local_metadata (dolt-ignored), not metadata
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store, err := embeddeddolt.Open(ctx, beadsDir, "basic", "main")
			if err != nil {
				t.Fatalf("failed to open store for bd_version check: %v", err)
			}
			defer store.Close()
			if val, err := store.GetLocalMetadata(ctx, "bd_version"); err != nil || val == "" {
				t.Error("bd_version local metadata not set")
			}
		}()
		importTime := readBack(t, beadsDir, "basic", "last_import_time", true)
		if importTime == "" {
			t.Error("last_import_time metadata not set")
		}
		if _, err := time.Parse(time.RFC3339, importTime); err != nil {
			t.Errorf("last_import_time not valid RFC3339: %q", importTime)
		}

		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load metadata.json: %v", err)
		}
		if cfg.Backend != configfile.BackendDolt {
			t.Errorf("Backend: got %q, want %q", cfg.Backend, configfile.BackendDolt)
		}
		if cfg.ProjectID == "" {
			t.Error("ProjectID should be set")
		}

		requireFile(t, filepath.Join(beadsDir, "config.yaml"))
		if _, err := os.Stat(filepath.Join(beadsDir, "interactions.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("interactions.jsonl should be created only when audit.enabled is true, got stat err %v", err)
		}
		requireFile(t, filepath.Join(dir, "AGENTS.md"))
		requireFile(t, filepath.Join(dir, ".agents", "skills", "beads", "SKILL.md"))
		requireFile(t, filepath.Join(dir, ".agents", "skills", "beads", "agents", "openai.yaml"))
		requireFile(t, filepath.Join(dir, ".codex", "config.toml"))
		requireFile(t, filepath.Join(dir, ".codex", "hooks.json"))
		// Cursor integration is auto-installed by bd init too (rules + hooks).
		requireFile(t, filepath.Join(dir, ".cursor", "rules", "beads.mdc"))
		requireFile(t, filepath.Join(dir, ".cursor", "hooks.json"))

		content, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore"))
		if err != nil {
			t.Fatalf("failed to read .beads/.gitignore: %v", err)
		}
		for _, pattern := range []string{"*.db", "dolt/", "bd.sock"} {
			if !strings.Contains(string(content), pattern) {
				t.Errorf(".gitignore missing pattern: %s", pattern)
			}
		}

		{
			out := bdDolt(t, bd, dir, "remote", "list")
			if strings.Contains(out, "origin") {
				t.Fatalf("init without git origin should not configure a Dolt remote; remote list:\n%s", out)
			}

			configYAML, err := os.ReadFile(filepath.Join(dir, ".beads", "config.yaml"))
			if err != nil {
				t.Fatalf("read config.yaml: %v", err)
			}
			if strings.Contains(string(configYAML), "sync.remote:") || strings.Contains(string(configYAML), "sync-remote:") {
				t.Fatalf("init without git origin should not persist sync.remote; config.yaml:\n%s", configYAML)
			}
		}
	})

	t.Run("database_with_prefix", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--database", "shared_db", "--prefix", "alpha")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load metadata.json: %v", err)
		}
		if cfg.DoltDatabase != "shared_db" {
			t.Errorf("DoltDatabase: got %q, want %q", cfg.DoltDatabase, "shared_db")
		}
		requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "shared_db", ".dolt"))
		if val := readBack(t, beadsDir, "shared_db", "issue_prefix", false); val != "alpha" {
			t.Errorf("issue_prefix: got %q, want %q", val, "alpha")
		}
	})

	t.Run("fork_auto_contributor", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		origin := filepath.Join(dir, "origin.git")
		upstream := filepath.Join(dir, "upstream.git")
		for _, bareRepo := range []string{origin, upstream} {
			if err := os.MkdirAll(bareRepo, 0755); err != nil {
				t.Fatalf("mkdir %s: %v", bareRepo, err)
			}
			cmd := exec.Command("git", "init", "--bare")
			cmd.Dir = bareRepo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git init --bare %s failed: %v\n%s", bareRepo, err, out)
			}
		}
		for name, url := range map[string]string{"origin": origin, "upstream": upstream} {
			cmd := exec.Command("git", "remote", "add", name, url)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git remote add %s failed: %v\n%s", name, err, out)
			}
		}

		out := runBDInit(t, bd, dir, "--prefix", "forkauto")
		if strings.Contains(out, "Fork detected") {
			t.Errorf("--quiet should suppress fork auto-routing output, got:\n%s", out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		planningDir := filepath.Join(dir, ".beads-planning")
		if val := readBack(t, beadsDir, "forkauto", "routing.mode", false); val != "auto" {
			t.Errorf("routing.mode: got %q, want %q", val, "auto")
		}
		if val := readBack(t, beadsDir, "forkauto", "routing.contributor", false); val != planningDir {
			t.Errorf("routing.contributor: got %q, want %q", val, planningDir)
		}
		if val := readBack(t, beadsDir, "forkauto", "sync.remote", false); val != "upstream" {
			t.Errorf("sync.remote: got %q, want %q", val, "upstream")
		}
		if _, err := os.Stat(filepath.Join(planningDir, ".beads")); err != nil {
			t.Errorf("planning .beads missing: %v", err)
		}

		// Regression: autoConfigureForkContributor must initialize the planning
		// Dolt schema, not just create the .beads directory. An uninitialized
		// store causes "Dolt server unreachable" on first use (e.g. bd migrate-personal).
		planningEmbeddedDir := filepath.Join(planningDir, ".beads", "embeddeddolt")
		if _, err := os.Stat(planningEmbeddedDir); err != nil {
			t.Errorf("planning embeddeddolt dir missing (planning store not pre-initialized): %v", err)
		}

		roleCmd := exec.Command("git", "config", "--get", "beads.role")
		roleCmd.Dir = dir
		roleOut, err := roleCmd.Output()
		if err != nil {
			t.Fatalf("git config --get beads.role failed: %v", err)
		}
		if role := strings.TrimSpace(string(roleOut)); role != "contributor" {
			t.Errorf("beads.role: got %q, want %q", role, "contributor")
		}
	})

	t.Run("git_origin_registered_as_dolt_remote", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "plain.git")
		runGitForBootstrapTest(t, "", "init", "--bare", "-b", "main", bareDir)

		seedDir := t.TempDir()
		initGitRepoAt(t, seedDir)
		runGitForBootstrapTest(t, seedDir, "branch", "-M", "main")
		runGitForBootstrapTest(t, seedDir, "commit", "--allow-empty", "-m", "init")
		runGitForBootstrapTest(t, seedDir, "remote", "add", "origin", "file://"+bareDir)
		runGitForBootstrapTest(t, seedDir, "push", "-u", "origin", "main")

		dir := t.TempDir()
		initGitRepoAt(t, dir)
		remoteURL := "file://" + bareDir
		runGitForBootstrapTest(t, dir, "remote", "add", "origin", remoteURL)

		runBDInit(t, bd, dir, "--prefix", "pg", "--skip-hooks", "--skip-agents")

		out := bdDolt(t, bd, dir, "remote", "list")
		if !strings.Contains(out, "origin") || !strings.Contains(out, remoteURL) {
			t.Fatalf("git origin should be registered as a Dolt remote %q; remote list:\n%s", remoteURL, out)
		}

		configYAML, err := os.ReadFile(filepath.Join(dir, ".beads", "config.yaml"))
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		if !strings.Contains(string(configYAML), remoteURL) {
			t.Fatalf("git origin should be persisted as sync.remote; config.yaml:\n%s", configYAML)
		}

		bdDolt(t, bd, dir, "push")
		ls := exec.Command("git", "ls-remote", remoteURL, "refs/dolt/data")
		lsOut, err := ls.CombinedOutput()
		if err != nil {
			t.Fatalf("git ls-remote refs/dolt/data failed: %v\n%s", err, lsOut)
		}
		if !strings.Contains(string(lsOut), "refs/dolt/data") {
			t.Fatalf("bd dolt push did not publish refs/dolt/data:\n%s", lsOut)
		}
	})

	// The #5068 refusal and consent paths, end to end.

	t.Run("dolt_push_consent_and_lazy_adoption", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "later-origin.git")
		runGitForBootstrapTest(t, "", "init", "--bare", "-b", "main", bareDir)
		remoteURL := "file://" + bareDir

		dir := t.TempDir()
		initGitRepoAt(t, dir)
		runGitForBootstrapTest(t, dir, "branch", "-M", "main")
		runGitForBootstrapTest(t, dir, "commit", "--allow-empty", "-m", "init")
		runBDInit(t, bd, dir, "--prefix", "late", "--skip-hooks", "--skip-agents")
		bdCreate(t, bd, dir, "Lazy remote adoption", "--type", "task")

		runGitForBootstrapTest(t, dir, "remote", "add", "origin", remoteURL)
		runGitForBootstrapTest(t, dir, "push", "-u", "origin", "main")

		// No TTY and no --yes: bd must refuse rather than derive a remote and
		// upload to it.
		{
			out := bdDoltFail(t, bd, dir, "push")
			if !strings.Contains(out, remoteURL) {
				t.Errorf("refusal did not name the remote it would have adopted; output:\n%s", out)
			}

			if list := bdDolt(t, bd, dir, "remote", "list"); strings.Contains(list, remoteURL) {
				t.Errorf("refused push still added the remote; remote list:\n%s", list)
			}
			configYAML, readErr := os.ReadFile(filepath.Join(dir, ".beads", "config.yaml"))
			if readErr == nil && strings.Contains(string(configYAML), remoteURL) {
				t.Errorf("refused push still persisted sync.remote; config.yaml:\n%s", configYAML)
			}
			if lsOut, lsErr := exec.Command("git", "ls-remote", remoteURL, "refs/dolt/data").Output(); lsErr == nil && len(strings.TrimSpace(string(lsOut))) != 0 {
				t.Errorf("refused push still uploaded issue history: %s", lsOut)
			}
		}

		// --yes is the scripted consent for git-origin adoption (#5068). The
		// capability this subtest covers is unchanged; only the consent is
		// new, and a test process has no TTY so adoption now fails closed
		// without it.
		ambientBare := filepath.Join(t.TempDir(), "ambient-origin.git")
		runGitForBootstrapTest(t, "", "init", "--bare", "-b", "main", ambientBare)
		ambientURL := "file://" + ambientBare
		ambientDir := t.TempDir()
		initGitRepoAt(t, ambientDir)
		runGitForBootstrapTest(t, ambientDir, "remote", "add", "origin", ambientURL)

		pushCmd := exec.Command(bd, "-C", dir, "dolt", "push", "--yes")
		pushCmd.Dir = ambientDir
		pushCmd.Env = bdEnv(ambientDir)
		if out, err := pushCmd.CombinedOutput(); err != nil {
			t.Fatalf("bd -C target dolt push failed: %v\n%s", err, out)
		}

		out := bdDolt(t, bd, dir, "remote", "list")
		if !strings.Contains(out, "origin") || !strings.Contains(out, remoteURL) {
			t.Fatalf("bd dolt push --yes should adopt later git origin %q; remote list:\n%s", remoteURL, out)
		}
		if strings.Contains(out, ambientURL) {
			t.Fatalf("bd -C target dolt push adopted ambient origin %q; remote list:\n%s", ambientURL, out)
		}

		configYAML, err := os.ReadFile(filepath.Join(dir, ".beads", "config.yaml"))
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		if !strings.Contains(string(configYAML), remoteURL) {
			t.Fatalf("bd dolt push should persist sync.remote; config.yaml:\n%s", configYAML)
		}

		ls := exec.Command("git", "ls-remote", remoteURL, "refs/dolt/data")
		lsOut, err := ls.CombinedOutput()
		if err != nil {
			t.Fatalf("git ls-remote refs/dolt/data failed: %v\n%s", err, lsOut)
		}
		if !strings.Contains(string(lsOut), "refs/dolt/data") {
			t.Fatalf("bd dolt push did not publish refs/dolt/data:\n%s", lsOut)
		}
	})

	t.Run("remote_bootstraps_existing_dolt_data", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses os.Symlink to mask dolt off PATH; symlink semantics differ on Windows")
		}
		remoteDir := filepath.Join(t.TempDir(), "remote")
		remoteURL := "file://" + remoteDir

		sourceDir, _, _ := bdInit(t, bd, "--prefix", "src", "--skip-hooks", "--skip-agents")
		sourceCfg, err := configfile.Load(filepath.Join(sourceDir, ".beads"))
		if err != nil {
			t.Fatalf("load source metadata.json: %v", err)
		}
		if sourceCfg.ProjectID == "" {
			t.Fatal("source project ID is empty")
		}

		cmd := exec.Command(bd, "create", "Remote issue", "--type", "task")
		cmd.Dir = sourceDir
		cmd.Env = bdEnv(sourceDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd create failed: %v\n%s", err, out)
		}
		bdDolt(t, bd, sourceDir, "commit")
		bdDolt(t, bd, sourceDir, "remote", "add", "origin", remoteURL)
		bdDolt(t, bd, sourceDir, "push", "--force")

		cloneDir := t.TempDir()
		initGitRepoAt(t, cloneDir)
		gitBin, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("git not found: %v", err)
		}
		pathDir := filepath.Join(t.TempDir(), "path")
		if err := os.MkdirAll(pathDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(gitBin, filepath.Join(pathDir, "git")); err != nil {
			t.Fatalf("symlink git into PATH: %v", err)
		}
		noDoltEnv := bdEnv(cloneDir)
		replacedPath := false
		for i, entry := range noDoltEnv {
			if strings.HasPrefix(entry, "PATH=") {
				noDoltEnv[i] = "PATH=" + pathDir
				replacedPath = true
				break
			}
		}
		if !replacedPath {
			noDoltEnv = append(noDoltEnv, "PATH="+pathDir)
		}
		cmd = exec.Command(bd, "init", "--quiet", "--prefix", "clone", "--remote", remoteURL, "--skip-hooks", "--skip-agents")
		cmd.Dir = cloneDir
		cmd.Env = noDoltEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd init --remote without dolt CLI failed: %v\n%s", err, out)
		}

		cloneCfg, err := configfile.Load(filepath.Join(cloneDir, ".beads"))
		if err != nil {
			t.Fatalf("load clone metadata.json: %v", err)
		}
		if cloneCfg.ProjectID != sourceCfg.ProjectID {
			t.Fatalf("clone ProjectID = %q, want source ProjectID %q", cloneCfg.ProjectID, sourceCfg.ProjectID)
		}
		if val := readBack(t, filepath.Join(cloneDir, ".beads"), "clone", "_project_id", true); val != sourceCfg.ProjectID {
			t.Fatalf("clone database _project_id = %q, want source ProjectID %q", val, sourceCfg.ProjectID)
		}

		cmd = exec.Command(bd, "list")
		cmd.Dir = cloneDir
		cmd.Env = bdEnv(cloneDir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd list failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Remote issue") {
			t.Fatalf("cloned database missing remote issue:\n%s", stdout.String())
		}

		cloneBeadsDir := filepath.Join(cloneDir, ".beads")
		requireNoFile(t, filepath.Join(cloneBeadsDir, "hooks"))
		requireNoFile(t, filepath.Join(cloneDir, "AGENTS.md"))
		requireNoFile(t, filepath.Join(cloneDir, "CLAUDE.md"))
		requireNoFile(t, filepath.Join(cloneDir, ".claude"))
		requireNoFile(t, filepath.Join(cloneDir, ".agents"))
		requireNoFile(t, filepath.Join(cloneDir, ".codex"))

		out := bdDolt(t, bd, cloneDir, "remote", "list")
		if !strings.Contains(out, "origin") || !strings.Contains(out, remoteURL) {
			t.Fatalf("expected origin remote %q in remote list:\n%s", remoteURL, out)
		}

		configYAML, err := os.ReadFile(filepath.Join(cloneBeadsDir, "config.yaml"))
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		if !strings.Contains(string(configYAML), remoteURL) {
			t.Fatalf("config.yaml should persist --remote URL %q:\n%s", remoteURL, configYAML)
		}
	})

	t.Run("remote_behind_schema_gate", func(t *testing.T) {
		// bd-4mpy7 / #4516: bootstrapping from a remote whose database is
		// behind this binary's schema. Shared fixture: a published remote
		// regressed one migration below LatestVersion. Two paths against it:
		// the default smart gate auto-migrates the clone as a safe
		// first-mover (remote at the same version — no one has migrated),
		// while the BD_SMART_GATE=0 opt-out must fail with
		// designated-migrator guidance and leave a finalized workspace where
		// the guidance commands can run — not a half-initialized directory
		// with a raw gate error.
		remoteDir := filepath.Join(t.TempDir(), "behind-remote")
		remoteURL := "file://" + remoteDir

		sourceDir, sourceBeads, _ := bdInit(t, bd, "--prefix", "bsrc", "--skip-hooks", "--skip-agents")
		bdCreate(t, bd, sourceDir, "Behind remote issue", "--type", "task")
		bdDolt(t, bd, sourceDir, "commit")

		// Regress the source database one migration and publish it, all in
		// one raw SQL session — running bd against the regressed database
		// would just auto-migrate it back (it has no remote registered yet).
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		db, cleanupSQL, err := embeddeddolt.OpenSQL(ctx, filepath.Join(sourceBeads, "embeddeddolt"), "bsrc", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		for _, q := range []string{
			fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", schema.LatestVersion()),
			"CALL DOLT_COMMIT('-am', 'regress schema one version')",
			fmt.Sprintf("CALL DOLT_REMOTE('add', 'origin', '%s')", remoteURL),
			"CALL DOLT_PUSH('--force', 'origin', 'main')",
		} {
			if _, err := db.ExecContext(ctx, q); err != nil {
				_ = cleanupSQL()
				t.Fatalf("%s: %v", q, err)
			}
		}
		_ = cleanupSQL()

		t.Run("default_smart_gate_auto_migrates_first_mover", func(t *testing.T) {
			cloneDir := t.TempDir()
			initGitRepoAt(t, cloneDir)
			cmd := exec.Command(bd, "init", "--quiet", "--prefix", "bclone", "--remote", remoteURL, "--skip-hooks", "--skip-agents")
			cmd.Dir = cloneDir
			// Exercise the true default: strip any ambient opt-out so
			// BD_SMART_GATE is genuinely unset.
			cmd.Env = envWithout(append(bdEnv(cloneDir), schema.AllowRemoteMigrateEnv+"=0"), schema.SmartGateEnv)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("default smart gate should auto-migrate the safe first-mover during init: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "Smart gate") || !strings.Contains(string(out), "bd dolt push") {
				t.Fatalf("smart auto-migrate should announce itself and direct a follow-up push:\n%s", out)
			}

			// The clone is migrated and immediately usable, no unlock needed.
			cmd = exec.Command(bd, "list")
			cmd.Dir = cloneDir
			cmd.Env = bdEnv(cloneDir)
			listOut, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("bd list after smart auto-migrate failed: %v\n%s", err, listOut)
			}
			if !strings.Contains(string(listOut), "Behind remote issue") {
				t.Fatalf("auto-migrated clone missing source issue:\n%s", listOut)
			}
		})

		t.Run("opt_out_gates_with_guidance", func(t *testing.T) {
			cloneDir := t.TempDir()
			initGitRepoAt(t, cloneDir)
			cmd := exec.Command(bd, "init", "--quiet", "--prefix", "bclone", "--remote", remoteURL, "--skip-hooks", "--skip-agents")
			cmd.Dir = cloneDir
			cmd.Env = append(bdEnv(cloneDir), schema.AllowRemoteMigrateEnv+"=0", schema.SmartGateEnv+"=0")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("bd init --remote against a behind-schema remote should fail; output:\n%s", out)
			}
			for _, want := range []string{
				"Re-running `bd init` will NOT fix this",
				"bd migrate --force",
				"bd dolt push",
			} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("init output missing %q:\n%s", want, out)
				}
			}

			// The failed init must leave a finalized workspace (metadata.json,
			// config.yaml) so the guidance commands can open the cloned database.
			cloneBeads := filepath.Join(cloneDir, ".beads")
			for _, f := range []string{"metadata.json", "config.yaml"} {
				if _, err := os.Stat(filepath.Join(cloneBeads, f)); err != nil {
					t.Fatalf("failed init should leave %s behind: %v", f, err)
				}
			}

			// Recovery per the guidance: the designated migrator unlocks,
			// migrates, and the workspace is usable.
			cmd = exec.Command(bd, "migrate")
			cmd.Dir = cloneDir
			cmd.Env = append(bdEnv(cloneDir), schema.AllowRemoteMigrateEnv+"=1")
			if migOut, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s=1 bd migrate failed: %v\n%s", schema.AllowRemoteMigrateEnv, err, migOut)
			}

			cmd = exec.Command(bd, "list")
			cmd.Dir = cloneDir
			cmd.Env = bdEnv(cloneDir)
			listOut, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("bd list after migrate failed: %v\n%s", err, listOut)
			}
			if !strings.Contains(string(listOut), "Behind remote issue") {
				t.Fatalf("migrated clone missing source issue:\n%s", listOut)
			}
		})
	})

	t.Run("remote_clone_failure_emits_url_and_hint", func(t *testing.T) {
		// remotesapi:// is rejected by dolt as an unknown scheme almost
		// instantly, so this exercises the non-empty-remote clone failure
		// path without depending on TCP timeouts. Verifies (a) init exits
		// non-zero rather than silently bootstrapping fresh, (b) the wrap
		// from cmd/bd/init.go echoes the URL the user typed in %q form,
		// and (c) the Hint: line is present.
		remoteURL := "remotesapi://127.0.0.1:1/no-such-db"
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bd, "init", "--quiet", "--prefix", "fail", "--remote", remoteURL, "--skip-hooks", "--skip-agents")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected bd init --remote with bogus URL to fail; got success:\n%s", out)
		}
		wantWrap := fmt.Sprintf("failed to clone remote %q", remoteURL)
		if !strings.Contains(string(out), wantWrap) {
			t.Fatalf("expected init.go wrap %q in output; got:\n%s", wantWrap, out)
		}
		if !strings.Contains(string(out), "Hint:") {
			t.Fatalf("expected error output to include a Hint: about reachability/credentials; got:\n%s", out)
		}
		if _, statErr := os.Stat(filepath.Join(dir, ".beads", "config.yaml")); statErr == nil {
			t.Fatalf(".beads/config.yaml should not exist after a failed clone; init must not silently fall through to fresh init")
		}
	})

	// Regression: bd init --stealth must not touch any git-visible files. Previously it
	// created/modified the tracked project-root .gitignore via doctor.EnsureProjectGitignore, which
	// showed up in `git status` and defeated stealth. Everything beads adds must be excluded
	// (.beads/) or live in .git/info/exclude, leaving the working tree clean from git's view.
	t.Run("stealth_leaves_worktree_clean", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "stealth.git")
		runGitForBootstrapTest(t, "", "init", "--bare", "-b", "main", bareDir)
		remoteURL := "file://" + bareDir

		dir := t.TempDir()
		initGitRepoAt(t, dir)
		runGitForBootstrapTest(t, dir, "remote", "add", "origin", remoteURL)

		// Commit a baseline so the repo has a clean, non-empty starting state.
		gitignorePath := filepath.Join(dir, ".gitignore")
		if err := os.WriteFile(gitignorePath, []byte("node_modules/\n"), 0644); err != nil {
			t.Fatalf("seed .gitignore: %v", err)
		}
		for _, args := range [][]string{
			{"add", "-A"},
			{"commit", "-m", "baseline"},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s failed: %v\n%s", args[0], err, out)
			}
		}

		runBDInit(t, bd, dir, "--prefix", "stc", "--stealth")

		beadsDir := filepath.Join(dir, ".beads")
		requireNoFile(t, filepath.Join(dir, "AGENTS.md"))
		requireNoFile(t, filepath.Join(dir, "CLAUDE.md"))
		requireNoFile(t, filepath.Join(dir, ".claude"))
		requireNoFile(t, filepath.Join(dir, ".agents"))
		requireNoFile(t, filepath.Join(dir, ".codex"))

		// Stealth must stay invisible: it should create .beads/ but route everything else into
		// .git/info/exclude so the database lives there without git seeing it.
		requireFile(t, beadsDir)
		excludeContent, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
		if err != nil {
			t.Fatalf("failed to read .git/info/exclude: %v", err)
		}
		for _, want := range []string{".beads/", ".dolt/", "*.db"} {
			if !strings.Contains(string(excludeContent), want) {
				t.Errorf(".git/info/exclude missing %q:\n%s", want, excludeContent)
			}
		}

		// git status --porcelain must be empty: stealth touched no visible files.
		cmd := exec.Command("git", "-c", "core.hooksPath=", "status", "--porcelain")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git status failed: %v\n%s", err, out)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Errorf("bd init --stealth left git-visible changes (should be invisible):\n%s", out)
		}

		// And the seeded .gitignore must be byte-for-byte unchanged.
		got, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if string(got) != "node_modules/\n" {
			t.Errorf("stealth modified project .gitignore:\ngot: %q", string(got))
		}

		{
			out := bdDolt(t, bd, dir, "remote", "list")
			if strings.Contains(out, "origin") {
				t.Fatalf("stealth init should not synthesize a Dolt remote; remote list:\n%s", out)
			}

			configYAML, err := os.ReadFile(filepath.Join(dir, ".beads", "config.yaml"))
			if err != nil {
				t.Fatalf("read config.yaml: %v", err)
			}
			if strings.Contains(string(configYAML), "sync.remote:") || strings.Contains(string(configYAML), "sync-remote:") {
				t.Fatalf("stealth init should not persist sync.remote; config.yaml:\n%s", configYAML)
			}
		}

		// bd doctor --fix may exit non-zero for unrelated checks; we only care that it does not
		// introduce git-visible changes on a stealth repo.
		{
			fixCmd := exec.Command(bd, "doctor", "--fix", "--yes")
			fixCmd.Dir = dir
			fixCmd.Env = bdEnv(dir)
			if out, err := fixCmd.CombinedOutput(); err != nil {
				t.Logf("bd doctor --fix exited non-zero (tolerated): %v\n%s", err, out)
			}

			statusCmd := exec.Command("git", "-c", "core.hooksPath=", "status", "--porcelain")
			statusCmd.Dir = dir
			out, err := statusCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git status failed: %v\n%s", err, out)
			}
			if strings.TrimSpace(string(out)) != "" {
				t.Errorf("bd doctor --fix left git-visible changes on a stealth repo:\n%s", out)
			}
			if got, _ := os.ReadFile(gitignorePath); string(got) != "node_modules/\n" {
				t.Errorf("bd doctor --fix modified project .gitignore on a stealth repo:\ngot: %q", string(got))
			}
		}
	})

	t.Run("force_reinit", func(t *testing.T) {
		doltBin, err := exec.LookPath("dolt")
		if err != nil {
			t.Skip("dolt CLI not on PATH")
		}

		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fi")
		dbDir := filepath.Join(beadsDir, "embeddeddolt", "fi")

		statusOut := runDolt(t, doltBin, dbDir, "status")
		if !strings.Contains(statusOut, "nothing to commit") {
			t.Errorf("after first init: expected clean working set, got:\n%s", statusOut)
		}
		logOut1 := runDolt(t, doltBin, dbDir, "log", "--oneline")
		for _, want := range []string{"schema: apply migrations", "bd init"} {
			if !strings.Contains(logOut1, want) {
				t.Errorf("after first init: missing %q commit:\n%s", want, logOut1)
			}
		}
		headAfterFirst := doltHeadHash(t, doltBin, dbDir)
		t.Logf("HEAD after first init: %s", headAfterFirst)
		t.Logf("log after first init:\n%s", logOut1)

		// Second init with --force
		runBDInit(t, bd, dir, "--prefix", "fi", "--force")

		statusOut = runDolt(t, doltBin, dbDir, "status")
		if !strings.Contains(statusOut, "nothing to commit") {
			t.Errorf("after force reinit: expected clean working set, got:\n%s", statusOut)
		}
		logOut2 := runDolt(t, doltBin, dbDir, "log", "--oneline")
		t.Logf("HEAD after force reinit: %s", doltHeadHash(t, doltBin, dbDir))
		t.Logf("log after force reinit:\n%s", logOut2)

		for _, want := range []string{"schema: apply migrations", "bd init"} {
			if !strings.Contains(logOut2, want) {
				t.Errorf("after force reinit: missing %q commit:\n%s", want, logOut2)
			}
		}

		commitCount1 := strings.Count(strings.TrimSpace(logOut1), "\n") + 1
		commitCount2 := strings.Count(strings.TrimSpace(logOut2), "\n") + 1
		if commitCount2 < commitCount1 {
			t.Errorf("commit count decreased after force reinit: before=%d after=%d", commitCount1, commitCount2)
		}
		if val := readBack(t, beadsDir, "fi", "issue_prefix", false); val != "fi" {
			t.Errorf("issue_prefix after --force: got %q, want %q", val, "fi")
		}
	})

	t.Run("auto_commit_bypasses_hooks", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		templatePath := filepath.Join(dir, "custom-agents.md")
		if err := os.WriteFile(templatePath, []byte("# Custom Agents\nThis is custom.\n"), 0644); err != nil {
			t.Fatal(err)
		}
		hookPath := filepath.Join(dir, ".git", "hooks", "prepare-commit-msg")
		hook := "#!/bin/sh\necho hook-fired >> .hook-ran\nexit 1\n"
		if err := os.WriteFile(hookPath, []byte(hook), 0755); err != nil {
			t.Fatal(err)
		}
		unsetHooksPath := exec.Command("git", "config", "--unset", "core.hooksPath")
		unsetHooksPath.Dir = dir
		if out, err := unsetHooksPath.CombinedOutput(); err != nil {
			t.Fatalf("git config --unset core.hooksPath failed: %v\n%s", err, out)
		}

		{
			cmd := exec.Command(bd, "init", "--prefix", "hook", "--agents-template", templatePath)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			if err != nil {
				t.Fatalf("bd init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "bd initialized successfully") {
				t.Errorf("expected success message, got: %s", stdout.String())
			}
		}

		content, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		if err != nil {
			t.Fatalf("failed to read AGENTS.md: %v", err)
		}
		if !strings.Contains(string(content), "Custom Agents") {
			t.Error("AGENTS.md should contain custom template content")
		}

		if _, err := os.Stat(filepath.Join(dir, ".hook-ran")); err == nil {
			t.Fatal("expected init auto-commit to bypass git hooks")
		}
		logCmd := exec.Command("git", "log", "--oneline", "-n", "1")
		logCmd.Dir = dir
		stdout, stderr, err := runCommandBuffers(t, logCmd)
		if err != nil {
			t.Fatalf("git log failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "bd init: initialize beads issue tracking") {
			t.Fatalf("expected init commit to succeed, got log: %s", stdout.String())
		}
	})

	t.Run("from_jsonl_with_remote_data_requires_discard_and_skips_clone", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "remote.git")
		runGitForBootstrapTest(t, "", "init", "--bare", bareDir)

		sourceDir := t.TempDir()
		runGitForBootstrapTest(t, sourceDir, "init", "-b", "main")
		runGitForBootstrapTest(t, sourceDir, "config", "user.email", "test@test.com")
		runGitForBootstrapTest(t, sourceDir, "config", "user.name", "Test User")
		runGitForBootstrapTest(t, sourceDir, "commit", "--allow-empty", "-m", "init")
		runGitForBootstrapTest(t, sourceDir, "remote", "add", "origin", bareDir)
		runGitForBootstrapTest(t, sourceDir, "push", "origin", "main")
		runGitForBootstrapTest(t, sourceDir, "push", "origin", "HEAD:refs/dolt/data")

		dir := t.TempDir()
		initGitRepoAt(t, dir)
		runGitForBootstrapTest(t, dir, "remote", "add", "origin", bareDir)

		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatal(err)
		}
		issue := types.Issue{
			ID:        "jlremote-abc123",
			Title:     "JSONL authoritative",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		line, _ := json.Marshal(issue)
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), append(line, '\n'), 0644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(bd, "init", "--prefix", "jlremote", "--from-jsonl", "--discard-remote", "--destroy-token=DESTROY-jlremote", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("--from-jsonl with authorized remote discard should import without cloning: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		showCmd := exec.Command(bd, "show", "jlremote-abc123", "--json")
		showCmd.Dir = dir
		showCmd.Env = bdEnv(dir)
		out, err := showCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("imported issue not found: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "JSONL authoritative") {
			t.Fatalf("imported issue title missing from show output:\n%s", out)
		}
	})

	t.Run("server_flags_ignored", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "sv",
			"--server-host", "10.0.0.1", "--server-port", "4444", "--server-user", "alice")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load metadata.json: %v", err)
		}
		if cfg.DoltServerHost != "10.0.0.1" {
			t.Errorf("DoltServerHost: got %q, want %q", cfg.DoltServerHost, "10.0.0.1")
		}
		if cfg.DoltServerPort != 4444 {
			t.Errorf("DoltServerPort: got %d, want %d", cfg.DoltServerPort, 4444)
		}
		if cfg.DoltServerUser != "alice" {
			t.Errorf("DoltServerUser: got %q, want %q", cfg.DoltServerUser, "alice")
		}
	})

	t.Run("invalid_dirname_errors_early", func(t *testing.T) {
		// A directory name like "my project" (space) survives hyphen/dot sanitization
		// and produces an invalid Dolt database name. The init command should exit
		// non-zero with a human-readable error rather than a cryptic storage failure.
		parent := t.TempDir()
		dir := filepath.Join(parent, "my project")
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		initGitRepoAt(t, dir)
		cmd := exec.Command(bd, "init", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("bd init should have failed for directory with invalid name")
		}
		outStr := string(out)
		if !strings.Contains(outStr, "invalid database name") && !strings.Contains(outStr, "produces an invalid") {
			t.Errorf("expected actionable error message, got: %s", outStr)
		}
	})

	t.Run("remote_host_without_server_mode_fails", func(t *testing.T) {
		// When dolt.host is set to a remote address but server mode is not
		// enabled, bd init must hard-fail (not fall through to embedded).
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		xdgDir := filepath.Join(dir, ".config", "bd")
		if err := os.MkdirAll(xdgDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(xdgDir, "config.yaml"),
			[]byte("dolt.host: 100.111.197.110\ndolt.port: 3306\n"), 0o600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}

		cmd := exec.Command(bd, "init", "--prefix", "ambi", "--non-interactive")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected bd init to fail with remote host and no server mode, but it succeeded:\n%s", out)
		}
		output := string(out)
		if !strings.Contains(output, "server mode is not enabled") {
			t.Errorf("expected error about server mode not enabled, got:\n%s", output)
		}
		if !strings.Contains(output, "100.111.197.110") {
			t.Errorf("error should mention the configured host, got:\n%s", output)
		}
	})

	t.Run("config_yaml_dolt_mode_server_metadata", func(t *testing.T) {
		// When dolt.mode: server is set in config.yaml and init runs in
		// embedded mode (no server available), the metadata.json should
		// still reflect that server mode was requested. We verify by
		// checking that the init process attempted server mode.
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		xdgDir := filepath.Join(dir, ".config", "bd")
		if err := os.MkdirAll(xdgDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(xdgDir, "config.yaml"),
			[]byte("dolt.mode: server\n"), 0o600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}

		// With dolt.mode: server and no actual server, init should fail
		// with a connection error — proving that config.yaml triggered
		// server mode.
		cmd := exec.Command(bd, "init", "--prefix", "srvmode", "--non-interactive", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		// We expect failure because there's no server to connect to.
		// The key assertion is that it tried server mode at all.
		if err == nil {
			// If it succeeded, it created an embedded DB — meaning
			// config.yaml dolt.mode was ignored.
			beadsDir := filepath.Join(dir, ".beads")
			cfg, loadErr := configfile.Load(beadsDir)
			if loadErr != nil {
				t.Fatalf("bd init succeeded but cannot load metadata: %v", loadErr)
			}
			if strings.ToLower(cfg.DoltMode) != "server" {
				t.Errorf("expected DoltMode=server in metadata, got %q (config.yaml dolt.mode: server was ignored)", cfg.DoltMode)
			}
		} else {
			// Init failed — check that the error is connection-related,
			// which proves server mode was attempted.
			output := string(out)
			if !strings.Contains(output, "connect") && !strings.Contains(output, "server") &&
				!strings.Contains(output, "dial") && !strings.Contains(output, "refused") {
				t.Errorf("expected server connection error, got:\n%s", output)
			}
		}
	})

	t.Run("config_yaml_server_mode_allows_hyphenated_database_name", func(t *testing.T) {
		// Server mode allows hyphens in database names. dolt.mode: server from
		// config.yaml must be applied before embedded-mode database validation.
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		xdgDir := filepath.Join(dir, ".config", "bd")
		if err := os.MkdirAll(xdgDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(xdgDir, "config.yaml"),
			[]byte("dolt.mode: server\n"), 0o600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}

		cmd := exec.Command(bd, "init", "--prefix", "hyphendb", "--database", "server-db", "--non-interactive", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected server init to fail without a server, but it succeeded:\n%s", out)
		}
		output := string(out)
		if strings.Contains(output, "hyphens which are invalid in embedded mode") {
			t.Fatalf("config.yaml dolt.mode: server was applied too late:\n%s", output)
		}
		if !strings.Contains(output, "connect") && !strings.Contains(output, "server") &&
			!strings.Contains(output, "dial") && !strings.Contains(output, "refused") {
			t.Errorf("expected server connection error, got:\n%s", output)
		}
	})
}

// TestEmbeddedInitConcurrent verifies concurrent `bd init` writers are
// serialized rather than corrupting the workspace. The EXCLUSIVE workspace
// gate (see acquireExclusiveWorkspaceGates in cmd/bd/init.go) is acquired
// before the embedded Dolt flock is ever attempted, so contention here
// normally surfaces as gate-busy output rather than a flock error; either is
// classified by isEmbeddedLockOutput as the same "another process holds the
// lock" outcome. At least one process must succeed; unexpected errors still
// fail the test.
//
// It deliberately does NOT require that any racer *observed* contention.
// Whether a waiter blocks and then succeeds or gives up and reports the gate
// busy depends on how long the winner holds the gate versus the waiter's wait
// budget — a property of the machine, not of the lock. On a runner fast enough
// that all ten inits serialize inside that budget, zero lock errors is the
// correct outcome: it means serialization worked and nobody had to give up.
// Requiring one made this test fail on exactly the hardware where the gate was
// working best (GH#4914), and the EXCLUSIVE gate added in #5093 — acquired
// before the embedded flock is ever attempted — makes the serialize-and-succeed
// outcome more likely, not less. Every other concurrency test in this package
// already treats contention as tolerated rather than required; this one was the
// outlier.
//
// The contention path itself is not left uncovered:
// TestInitGateBusyClassifiedAsLockContention (added alongside the gate in
// #5093) exercises it deterministically by holding the gate in-process, which
// is what this test was approximating by racing.
func TestEmbeddedInitConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()
	initGitRepoAt(t, dir)

	const N = 10
	env := bdEnv(dir)

	type result struct {
		idx      int
		out      string
		err      error
		timedOut bool
	}
	results := make([]result, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, bd, "init", "--prefix", "conc", "--force", "--quiet", "--skip-agents")
			cmd.Dir = dir
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			results[idx] = result{idx: idx, out: string(out), err: err, timedOut: ctx.Err() == context.DeadlineExceeded}
		}(i)
	}
	wg.Wait()

	successes, lockErrors, timeoutKills := 0, 0, 0
	for _, r := range results {
		if r.timedOut {
			t.Logf("process %d timed out after 90s running concurrent bd init: %v\n%s", r.idx, r.err, r.out)
			timeoutKills++
			continue
		}
		if strings.Contains(r.out, "panic") {
			t.Errorf("process %d panicked:\n%s", r.idx, r.out)
		}
		if r.err == nil {
			successes++
		} else if isEmbeddedLockOutput(r.out) {
			lockErrors++
		} else {
			t.Errorf("process %d failed with unexpected error: %v\n%s", r.idx, r.err, r.out)
		}
	}
	if successes < 1 {
		t.Errorf("expected at least 1 success, got %d", successes)
	}
	// No assertion on lockErrors: see the doc comment. 0 is a valid outcome.
	// timeoutKills > 2 (i.e. > N/5) indicates a systemic runner problem, not normal load variance.
	if timeoutKills > 2 {
		t.Errorf("too many timeout-killed processes: %d/%d (cap is 2)", timeoutKills, N)
	}
	if successes+lockErrors+timeoutKills != N {
		t.Errorf("expected successes (%d) + lock errors (%d) + timeout kills (%d) = %d, got %d",
			successes, lockErrors, timeoutKills, N, successes+lockErrors+timeoutKills)
	}
	t.Logf("%d/%d succeeded, %d/%d got lock error, %d/%d timed out", successes, N, lockErrors, N, timeoutKills, N)

	beadsDir := filepath.Join(dir, ".beads")
	embeddedDir := filepath.Join(beadsDir, "embeddeddolt")
	requireFile(t, embeddedDir)
	requireFile(t, filepath.Join(embeddedDir, "conc", ".dolt"))

	if val := readBack(t, beadsDir, "conc", "issue_prefix", false); val != "conc" {
		t.Errorf("issue_prefix: got %q, want %q", val, "conc")
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to load metadata.json: %v", err)
	}
	if cfg.Backend != configfile.BackendDolt {
		t.Errorf("Backend: got %q, want %q", cfg.Backend, configfile.BackendDolt)
	}

	if doltBin, err := exec.LookPath("dolt"); err == nil {
		dbDir := filepath.Join(embeddedDir, "conc")
		statusOut := runDolt(t, doltBin, dbDir, "status")
		if !strings.Contains(statusOut, "nothing to commit") {
			t.Errorf("expected clean working set after concurrent init, got:\n%s", statusOut)
		}
		logOut := runDolt(t, doltBin, dbDir, "log", "--oneline")
		if !strings.Contains(logOut, "schema: apply migrations") {
			t.Errorf("missing 'schema: apply migrations' commit:\n%s", logOut)
		}
	}
}

// TestInitGateBusyClassifiedAsLockContention pins the fix for
// TestEmbeddedInitConcurrent's regression: a losing concurrent `bd init`
// that fails because another process holds the workspace gate EXCLUSIVELY
// (rather than hitting the embedded Dolt flock, which the gate now
// short-circuits before it is ever attempted) must still be classified as
// lock contention by isEmbeddedLockOutput, not as an unexpected failure.
// Deterministic and fast: it holds the gate in-process instead of racing
// subprocesses against a wall-clock deadline, so unlike
// TestEmbeddedInitConcurrent it does not depend on the winner's init being
// slow enough to exhaust another process's wait budget.
func TestInitGateBusyClassifiedAsLockContention(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)

	oldWait := exclusiveGateWait
	exclusiveGateWait = 10 * time.Millisecond
	t.Cleanup(func() { exclusiveGateWait = oldWait })

	// Simulate the winner: hold the workspace gate EXCLUSIVELY for the
	// duration of its init, exactly as cmd/bd/init.go does.
	winner, err := acquireExclusiveWorkspaceGates(context.Background(), beadsDir, "test winner init")
	if err != nil {
		t.Fatalf("winner acquisition: %v", err)
	}
	defer func() { _ = winner.Release() }()

	// Simulate the loser: bd init's own acquisition call, and its own
	// error-wrapping (cmd/bd/init.go: "bd init refuses to run over live bd
	// activity on this workspace: %w").
	_, gateErr := acquireExclusiveWorkspaceGates(context.Background(), beadsDir, "bd init")
	if gateErr == nil {
		t.Fatal("loser acquisition under a live exclusive holder must fail")
	}
	loserOutput := fmt.Errorf("bd init refuses to run over live bd activity on this workspace: %w", gateErr).Error()

	if !isEmbeddedLockOutput(loserOutput) {
		t.Fatalf("gate-busy loser output not classified as lock contention: %q", loserOutput)
	}
}
