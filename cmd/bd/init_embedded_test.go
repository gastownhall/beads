package main

import (
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
		tmpDir, err := os.MkdirTemp("", "bd-embedded-init-test-*")
		if err != nil {
			embeddedBDErr = fmt.Errorf("failed to create temp dir: %w", err)
			return
		}
		name := "bd"
		if runtime.GOOS == "windows" {
			name = "bd.exe"
		}
		embeddedBD = filepath.Join(tmpDir, name)
		cmd := exec.Command("go", "build", "-o", embeddedBD, ".")
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
		{"config", "core.hooksPath", "/dev/null"},
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
	return append(env, "HOME="+dir, "BEADS_DOLT_AUTO_START=0", "BEADS_NO_DAEMON=1")
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd init %s failed: %v\n%s", strings.Join(extraArgs, " "), err, out)
	}
	return string(out)
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
	store, err := embeddeddolt.New(ctx, beadsDir, database, "main")
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
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "basic")
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
		_ = dir
	})

	t.Run("prefix", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "myproj")
		if val := readBack(t, beadsDir, "myproj", "issue_prefix", false); val != "myproj" {
			t.Errorf("issue_prefix: got %q, want %q", val, "myproj")
		}
	})

	t.Run("prefix_trailing_hyphen", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "test-")
		if val := readBack(t, beadsDir, "test", "issue_prefix", false); val != "test" {
			t.Errorf("issue_prefix: got %q, want %q", val, "test")
		}
	})

	t.Run("quiet", func(t *testing.T) {
		_, _, out := bdInit(t, bd, "--prefix", "qt")
		if strings.Contains(out, "bd initialized") {
			t.Error("--quiet should suppress success message")
		}
	})

	t.Run("not_quiet", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		cmd := exec.Command(bd, "init", "--prefix", "nq")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "bd initialized successfully") {
			t.Errorf("expected success message, got: %s", out)
		}
	})

	t.Run("database", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--database", "custom_db")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load metadata.json: %v", err)
		}
		if cfg.DoltDatabase != "custom_db" {
			t.Errorf("DoltDatabase: got %q, want %q", cfg.DoltDatabase, "custom_db")
		}
		requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "custom_db", ".dolt"))
		if val := readBack(t, beadsDir, "custom_db", "issue_prefix", false); val == "" {
			t.Error("issue_prefix not set in custom_db")
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
		if val := readBack(t, beadsDir, "shared_db", "issue_prefix", false); val != "alpha" {
			t.Errorf("issue_prefix: got %q, want %q", val, "alpha")
		}
	})

	t.Run("skip_hooks", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "sh", "--skip-hooks")
		requireNoFile(t, filepath.Join(beadsDir, "hooks"))
	})

	t.Run("stealth", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "st", "--stealth")
		requireNoFile(t, filepath.Join(dir, "AGENTS.md"))
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

	t.Run("setup_exclude", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "se", "--setup-exclude")
		content, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
		if err != nil {
			t.Fatalf("failed to read .git/info/exclude: %v", err)
		}
		if !strings.Contains(string(content), ".beads") {
			t.Error("--setup-exclude should add .beads to .git/info/exclude")
		}
	})

	t.Run("from_jsonl", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatal(err)
		}
		issues := []types.Issue{
			{ID: "jl-abc123", Title: "One", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			{ID: "jl-def456", Title: "Two", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		}
		var lines []string
		for _, issue := range issues {
			b, _ := json.Marshal(issue)
			lines = append(lines, string(b))
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(bd, "init", "--prefix", "jl", "--from-jsonl", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("--from-jsonl should succeed now that CreateIssuesWithFullOptions is implemented: %v\n%s", err, out)
		}
	})

	t.Run("backend_dolt", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "bdolt", "--backend", "dolt")
		embeddedDir := filepath.Join(beadsDir, "embeddeddolt")
		requireFile(t, embeddedDir)
		requireFile(t, filepath.Join(embeddedDir, "bdolt", ".dolt"))
	})

	t.Run("rejected_backends", func(t *testing.T) {
		for _, tc := range []struct {
			backend, wantErr string
		}{
			{"sqlite", "DEPRECATED"},
			{"postgres", "unknown backend"},
		} {
			out := bdInitFail(t, bd, "--backend", tc.backend)
			if !strings.Contains(out, tc.wantErr) {
				t.Errorf("--backend %s: expected %q, got: %s", tc.backend, tc.wantErr, out)
			}
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

	t.Run("metadata_written", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "meta")
		if val := readBack(t, beadsDir, "meta", "bd_version", true); val == "" {
			t.Error("bd_version metadata not set")
		}
		importTime := readBack(t, beadsDir, "meta", "last_import_time", true)
		if importTime == "" {
			t.Error("last_import_time metadata not set")
		}
		if _, err := time.Parse(time.RFC3339, importTime); err != nil {
			t.Errorf("last_import_time not valid RFC3339: %q", importTime)
		}
	})

	t.Run("metadata_json", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "mj")
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
	})

	t.Run("files_created", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fc", "--skip-hooks")
		requireFile(t, filepath.Join(beadsDir, "config.yaml"))
		requireFile(t, filepath.Join(beadsDir, "interactions.jsonl"))
		requireFile(t, filepath.Join(dir, "AGENTS.md"))

		content, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore"))
		if err != nil {
			t.Fatalf("failed to read .beads/.gitignore: %v", err)
		}
		for _, pattern := range []string{"*.db", "dolt/", "bd.sock"} {
			if !strings.Contains(string(content), pattern) {
				t.Errorf(".gitignore missing pattern: %s", pattern)
			}
		}
	})

	t.Run("agents_template", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		templatePath := filepath.Join(dir, "custom-agents.md")
		if err := os.WriteFile(templatePath, []byte("# Custom Agents\nThis is custom.\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runBDInit(t, bd, dir, "--prefix", "at", "--agents-template", templatePath, "--skip-hooks")
		content, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		if err != nil {
			t.Fatalf("failed to read AGENTS.md: %v", err)
		}
		if !strings.Contains(string(content), "Custom Agents") {
			t.Error("AGENTS.md should contain custom template content")
		}
	})

	t.Run("no_git_repo", func(t *testing.T) {
		dir := t.TempDir()
		// Don't init git — bd init should create one
		args := []string{"init", "--prefix", "ng", "--quiet"}
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd init (no git) failed: %v\n%s", err, out)
		}
		requireFile(t, filepath.Join(dir, ".git"))
	})

	t.Run("database_name_validation", func(t *testing.T) {
		out := bdInitFail(t, bd, "--database", "has spaces!")
		if !strings.Contains(out, "invalid database name") {
			t.Errorf("expected 'invalid database name' error, got: %s", out)
		}
	})

	t.Run("prefix_auto_detect_from_dirname", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "myproject")
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		initGitRepoAt(t, dir)
		runBDInit(t, bd, dir)
		if val := readBack(t, filepath.Join(dir, ".beads"), "myproject", "issue_prefix", false); val != "myproject" {
			t.Errorf("auto-detected issue_prefix: got %q, want %q", val, "myproject")
		}
	})

	t.Run("prefix_numeric_sanitized", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "001")
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		initGitRepoAt(t, dir)
		runBDInit(t, bd, dir)
		if val := readBack(t, filepath.Join(dir, ".beads"), "bd_001", "issue_prefix", false); val != "bd_001" {
			t.Errorf("sanitized issue_prefix: got %q, want %q", val, "bd_001")
		}
	})
}

// TestEmbeddedInitConcurrent verifies the exclusive flock prevents concurrent
// writers. Exactly one process should succeed; the rest get the lock error.
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
		idx int
		out string
		err error
	}
	results := make([]result, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			cmd := exec.Command(bd, "init", "--prefix", "conc", "--force", "--quiet")
			cmd.Dir = dir
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			results[idx] = result{idx: idx, out: string(out), err: err}
		}(i)
	}
	wg.Wait()

	successes, lockErrors := 0, 0
	for _, r := range results {
		if strings.Contains(r.out, "panic") {
			t.Errorf("process %d panicked:\n%s", r.idx, r.out)
		}
		if r.err == nil {
			successes++
		} else if strings.Contains(r.out, "one writer at a time") {
			lockErrors++
		} else {
			t.Errorf("process %d failed with unexpected error: %v\n%s", r.idx, r.err, r.out)
		}
	}
	if successes < 1 {
		t.Errorf("expected at least 1 success, got %d", successes)
	}
	if successes+lockErrors != N {
		t.Errorf("expected successes (%d) + lock errors (%d) = %d, got %d", successes, lockErrors, N, successes+lockErrors)
	}
	t.Logf("%d/%d succeeded, %d/%d got lock error", successes, N, lockErrors, N)

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
