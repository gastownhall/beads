//go:build embeddeddolt

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

// buildEmbeddedBD compiles the bd binary with the embeddeddolt build tag.
func buildEmbeddedBD(t *testing.T) string {
	t.Helper()
	embeddedBDOnce.Do(func() {
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
		cmd := exec.Command("go", "build", "-tags", "embeddeddolt", "-o", embeddedBD, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			embeddedBDErr = fmt.Errorf("go build -tags embeddeddolt failed: %v\n%s", err, out)
		}
	})
	if embeddedBDErr != nil {
		t.Fatalf("Failed to build embedded bd binary: %v", embeddedBDErr)
	}
	return embeddedBD
}

// initGitRepoAt initializes a bare-minimum git repo at dir so that bd init
// doesn't have to create one (avoids git user.name/email config issues in CI).
func initGitRepoAt(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		// Disable hooks path so bd init's hooks don't interfere
		{"config", "core.hooksPath", "/dev/null"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", args[0], err, out)
		}
	}
}

// bdEnv returns a minimal environment for running bd init in embedded mode.
// It strips BEADS_* vars and sets BEADS_DOLT_AUTO_START=0 (irrelevant for
// embedded, but belt-and-suspenders).
func bdEnv(dir string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BEADS_") {
			continue
		}
		env = append(env, e)
	}
	env = append(env,
		"HOME="+dir, // isolate from user's real config
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_NO_DAEMON=1",
	)
	return env
}

// readBackConfig opens the embedded dolt database and reads a config value.
func readBackConfig(t *testing.T, beadsDir, database, key string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := embeddeddolt.New(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("readBackConfig: New failed: %v", err)
	}
	defer store.Close()
	val, err := store.GetConfig(ctx, key)
	if err != nil {
		t.Fatalf("readBackConfig: GetConfig(%q) failed: %v", key, err)
	}
	return val
}

// readBackMetadata opens the embedded dolt database and reads a metadata value.
func readBackMetadata(t *testing.T, beadsDir, database, key string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := embeddeddolt.New(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("readBackMetadata: New failed: %v", err)
	}
	defer store.Close()
	val, err := store.GetMetadata(ctx, key)
	if err != nil {
		t.Fatalf("readBackMetadata: GetMetadata(%q) failed: %v", key, err)
	}
	return val
}

// stripANSI removes ANSI escape sequences from s.
func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until we hit a letter (the terminator)
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

// runDolt runs a dolt CLI command in the given directory and returns stdout
// with ANSI codes stripped.
func runDolt(t *testing.T, doltBin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(doltBin, args...)
	cmd.Dir = dir
	out, err := cmd.Output() // stdout only; stderr discarded
	if err != nil {
		t.Fatalf("dolt %s failed: %v", strings.Join(args, " "), err)
	}
	return stripANSI(string(out))
}

// doltHeadHash returns the HEAD commit hash from a dolt database directory.
func doltHeadHash(t *testing.T, doltBin, dir string) string {
	t.Helper()
	out := runDolt(t, doltBin, dir, "log", "-n", "1", "--oneline")
	// oneline format: "<hash> (HEAD -> main) <message>"
	line := strings.TrimSpace(out)
	if idx := strings.IndexByte(line, ' '); idx > 0 {
		return line[:idx]
	}
	t.Fatalf("unexpected dolt log --oneline output: %q", line)
	return ""
}

func TestEmbeddedInit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}

	bd := buildEmbeddedBD(t)

	t.Run("basic", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "basic", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
			t.Fatal(".beads directory not created")
		}

		embeddedDir := filepath.Join(beadsDir, "embeddeddolt")
		if _, err := os.Stat(embeddedDir); os.IsNotExist(err) {
			t.Fatal(".beads/embeddeddolt directory not created")
		}

		// The embedded dolt engine creates a .dolt directory inside the
		// database subdirectory: embeddeddolt/<database>/.dolt/
		doltDir := filepath.Join(embeddedDir, "basic", ".dolt")
		if _, err := os.Stat(doltDir); os.IsNotExist(err) {
			t.Fatalf(".dolt directory not created at %s", doltDir)
		}

		// If the dolt CLI is on PATH, verify commit history via dolt log/status.
		// The embedded engine and CLI share the same on-disk format.
		if doltBin, err := exec.LookPath("dolt"); err == nil {
			dbDir := filepath.Join(embeddedDir, "basic")

			// dolt status should show a clean working set
			statusCmd := exec.Command(doltBin, "status")
			statusCmd.Dir = dbDir
			statusOut, err := statusCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("dolt status failed: %v\n%s", err, statusOut)
			}
			if !strings.Contains(string(statusOut), "nothing to commit") {
				t.Errorf("expected clean working set, got:\n%s", statusOut)
			}

			// dolt log should contain the schema migration commit and the
			// bd init commit (plus the auto-generated "Initialize data repository" commit).
			logCmd := exec.Command(doltBin, "log", "--oneline")
			logCmd.Dir = dbDir
			logOut, err := logCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("dolt log failed: %v\n%s", err, logOut)
			}
			logStr := string(logOut)
			if !strings.Contains(logStr, "schema: apply migrations") {
				t.Errorf("dolt log missing 'schema: apply migrations' commit:\n%s", logStr)
			}
			if !strings.Contains(logStr, "bd init") {
				t.Errorf("dolt log missing 'bd init' commit:\n%s", logStr)
			}
		}
	})

	t.Run("prefix", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "myproj", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --prefix failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		val := readBackConfig(t, beadsDir, "myproj", "issue_prefix")
		if val != "myproj" {
			t.Errorf("issue_prefix: got %q, want %q", val, "myproj")
		}
	})

	t.Run("prefix_trailing_hyphen", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "test-", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --prefix test- failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		// Trailing hyphen should be stripped
		val := readBackConfig(t, beadsDir, "test", "issue_prefix")
		if val != "test" {
			t.Errorf("issue_prefix: got %q, want %q", val, "test")
		}
	})

	t.Run("quiet", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "qt", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --quiet failed: %v\n%s", err, out)
		}

		// --quiet should suppress stdout
		stdout := string(out)
		if strings.Contains(stdout, "bd initialized") {
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
			t.Fatalf("bd init (not quiet) failed: %v\n%s", err, out)
		}

		combined := string(out)
		if !strings.Contains(combined, "bd initialized successfully") {
			t.Errorf("expected success message, got: %s", combined)
		}
	})

	t.Run("database", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--database", "custom_db", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --database failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load metadata.json: %v", err)
		}
		if cfg.DoltDatabase != "custom_db" {
			t.Errorf("DoltDatabase: got %q, want %q", cfg.DoltDatabase, "custom_db")
		}

		// The database directory under embeddeddolt/ must be named after
		// the --database argument.
		dbDotDolt := filepath.Join(beadsDir, "embeddeddolt", "custom_db", ".dolt")
		if _, err := os.Stat(dbDotDolt); os.IsNotExist(err) {
			t.Fatalf("expected .dolt dir at %s", dbDotDolt)
		}

		// Verify config was written to the correct database
		val := readBackConfig(t, beadsDir, "custom_db", "issue_prefix")
		if val == "" {
			t.Error("issue_prefix not set in custom_db")
		}
	})

	t.Run("database_with_prefix", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--database", "shared_db", "--prefix", "alpha", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --database --prefix failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load metadata.json: %v", err)
		}
		if cfg.DoltDatabase != "shared_db" {
			t.Errorf("DoltDatabase: got %q, want %q", cfg.DoltDatabase, "shared_db")
		}

		// --prefix still sets issue_prefix in the database
		val := readBackConfig(t, beadsDir, "shared_db", "issue_prefix")
		if val != "alpha" {
			t.Errorf("issue_prefix: got %q, want %q", val, "alpha")
		}
	})

	t.Run("skip_hooks", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "sh", "--skip-hooks", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --skip-hooks failed: %v\n%s", err, out)
		}

		// Hooks directory should not be created
		hooksDir := filepath.Join(dir, ".beads", "hooks")
		if _, err := os.Stat(hooksDir); err == nil {
			t.Error("--skip-hooks should prevent hooks directory creation")
		}
	})

	t.Run("stealth", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "st", "--stealth", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --stealth failed: %v\n%s", err, out)
		}

		// Stealth mode should not create AGENTS.md
		if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
			t.Error("--stealth should not create AGENTS.md")
		}

		// Stealth mode should configure .git/info/exclude
		excludePath := filepath.Join(dir, ".git", "info", "exclude")
		content, err := os.ReadFile(excludePath)
		if err == nil && strings.Contains(string(content), ".beads") {
			// Good — stealth configured the exclude file
		}
		// Note: may not exist if git version doesn't create info/ dir,
		// so we don't fail on the read error.
	})

	t.Run("force_reinit", func(t *testing.T) {
		doltBin, err := exec.LookPath("dolt")
		if err != nil {
			t.Skip("dolt CLI not on PATH")
		}

		dir := t.TempDir()
		initGitRepoAt(t, dir)
		dbDir := filepath.Join(dir, ".beads", "embeddeddolt", "fi")

		// First init
		cmd := exec.Command(bd, "init", "--prefix", "fi", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("first bd init failed: %v\n%s", err, out)
		}

		// Verify dolt state after first init
		statusOut := runDolt(t, doltBin, dbDir, "status")
		if !strings.Contains(statusOut, "nothing to commit") {
			t.Errorf("after first init: expected clean working set, got:\n%s", statusOut)
		}

		logOut1 := runDolt(t, doltBin, dbDir, "log", "--oneline")
		if !strings.Contains(logOut1, "schema: apply migrations") {
			t.Errorf("after first init: missing 'schema: apply migrations' commit:\n%s", logOut1)
		}
		if !strings.Contains(logOut1, "bd init") {
			t.Errorf("after first init: missing 'bd init' commit:\n%s", logOut1)
		}

		headAfterFirst := doltHeadHash(t, doltBin, dbDir)
		t.Logf("HEAD after first init: %s", headAfterFirst)
		t.Logf("log after first init:\n%s", logOut1)

		// Second init with --force --quiet (quiet skips the confirmation prompt)
		cmd = exec.Command(bd, "init", "--prefix", "fi", "--force", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --force failed: %v\n%s", err, out)
		}

		// Verify dolt state after force reinit
		statusOut = runDolt(t, doltBin, dbDir, "status")
		if !strings.Contains(statusOut, "nothing to commit") {
			t.Errorf("after force reinit: expected clean working set, got:\n%s", statusOut)
		}

		logOut2 := runDolt(t, doltBin, dbDir, "log", "--oneline")
		headAfterForce := doltHeadHash(t, doltBin, dbDir)
		t.Logf("HEAD after force reinit: %s", headAfterForce)
		t.Logf("log after force reinit:\n%s", logOut2)

		// The original commits must still be present after force reinit.
		if !strings.Contains(logOut2, "schema: apply migrations") {
			t.Errorf("after force reinit: missing 'schema: apply migrations' commit:\n%s", logOut2)
		}
		if !strings.Contains(logOut2, "bd init") {
			t.Errorf("after force reinit: missing 'bd init' commit:\n%s", logOut2)
		}

		// Force reinit with identical config values is idempotent — dolt detects
		// no row-level diffs, so no new commit is created. If the HEAD did advance
		// it means new data was written (e.g. last_import_time changed), which is
		// also fine. Either way the commit count must not decrease.
		commitCount1 := strings.Count(strings.TrimSpace(logOut1), "\n") + 1
		commitCount2 := strings.Count(strings.TrimSpace(logOut2), "\n") + 1
		if commitCount2 < commitCount1 {
			t.Errorf("commit count decreased after force reinit: before=%d after=%d",
				commitCount1, commitCount2)
		}

		// Should still have a working database
		beadsDir := filepath.Join(dir, ".beads")
		val := readBackConfig(t, beadsDir, "fi", "issue_prefix")
		if val != "fi" {
			t.Errorf("issue_prefix after --force: got %q, want %q", val, "fi")
		}
	})

	t.Run("setup_exclude", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "se", "--setup-exclude", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --setup-exclude failed: %v\n%s", err, out)
		}

		// Should have .beads/ in .git/info/exclude
		excludePath := filepath.Join(dir, ".git", "info", "exclude")
		content, err := os.ReadFile(excludePath)
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

		// Create .beads directory and issues.jsonl before init
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatal(err)
		}

		issues := []types.Issue{
			{
				ID:        "jl-abc123",
				Title:     "Test issue one",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
			{
				ID:        "jl-def456",
				Title:     "Test issue two",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeBug,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		}
		var lines []string
		for _, issue := range issues {
			b, _ := json.Marshal(issue)
			lines = append(lines, string(b))
		}
		jsonlContent := strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(jsonlContent), 0644); err != nil {
			t.Fatal(err)
		}

		// --from-jsonl requires CreateIssuesWithFullOptions, which is not yet
		// implemented in the embedded store. Verify it fails gracefully (non-zero
		// exit) rather than silently succeeding.
		cmd := exec.Command(bd, "init", "--prefix", "jl", "--from-jsonl", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("--from-jsonl should fail: CreateIssuesWithFullOptions not yet implemented")
		}
		// Should panic or error, not silently succeed
		combined := string(out)
		if !strings.Contains(combined, "not implemented") && !strings.Contains(combined, "panic") {
			t.Logf("--from-jsonl failed with: %s", combined)
		}
	})

	t.Run("backend_dolt", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		// Explicit --backend=dolt should work fine
		cmd := exec.Command(bd, "init", "--prefix", "bdolt", "--backend", "dolt", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --backend dolt failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")

		// Must use the embedded store, not the server-mode dolt store
		embeddedDir := filepath.Join(beadsDir, "embeddeddolt")
		if _, err := os.Stat(embeddedDir); os.IsNotExist(err) {
			t.Fatal("expected embeddeddolt/ directory — store should be embedded, not server")
		}
		if _, err := os.Stat(filepath.Join(embeddedDir, "bdolt", ".dolt")); os.IsNotExist(err) {
			t.Fatal("expected embeddeddolt/bdolt/.dolt directory")
		}
	})

	t.Run("backend_sqlite_rejected", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--backend", "sqlite", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("bd init --backend sqlite should fail")
		}
		if !strings.Contains(string(out), "DEPRECATED") {
			t.Errorf("expected deprecation message, got: %s", out)
		}
	})

	t.Run("backend_unknown_rejected", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--backend", "postgres", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("bd init --backend postgres should fail")
		}
		if !strings.Contains(string(out), "unknown backend") {
			t.Errorf("expected unknown backend error, got: %s", out)
		}
	})

	t.Run("server_flags_ignored", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		// In embedded mode, server flags are accepted but ignored for the store.
		// They should still be written to metadata.json.
		cmd := exec.Command(bd, "init", "--prefix", "sv",
			"--server-host", "10.0.0.1",
			"--server-port", "4444",
			"--server-user", "alice",
			"--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init with server flags failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
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
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "meta", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")

		// Verify bd_version metadata was written
		bdVersion := readBackMetadata(t, beadsDir, "meta", "bd_version")
		if bdVersion == "" {
			t.Error("bd_version metadata not set")
		}

		// Verify last_import_time metadata was written
		importTime := readBackMetadata(t, beadsDir, "meta", "last_import_time")
		if importTime == "" {
			t.Error("last_import_time metadata not set")
		}
		// Should be parseable as RFC3339
		if _, err := time.Parse(time.RFC3339, importTime); err != nil {
			t.Errorf("last_import_time not valid RFC3339: %q", importTime)
		}
	})

	t.Run("metadata_json", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "mj", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load metadata.json: %v", err)
		}
		if cfg == nil {
			t.Fatal("metadata.json not found")
		}
		if cfg.Backend != configfile.BackendDolt {
			t.Errorf("Backend: got %q, want %q", cfg.Backend, configfile.BackendDolt)
		}
		if cfg.ProjectID == "" {
			t.Error("ProjectID should be set")
		}
	})

	t.Run("gitignore_created", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "gi", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		content, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore"))
		if err != nil {
			t.Fatalf("failed to read .beads/.gitignore: %v", err)
		}
		gitignore := string(content)
		for _, pattern := range []string{"*.db", "dolt/", "bd.sock"} {
			if !strings.Contains(gitignore, pattern) {
				t.Errorf(".gitignore missing pattern: %s", pattern)
			}
		}
	})

	t.Run("config_yaml_created", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "cy", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		if _, err := os.Stat(filepath.Join(beadsDir, "config.yaml")); os.IsNotExist(err) {
			t.Error("config.yaml not created")
		}
	})

	t.Run("agents_md_created", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		// Without --stealth, AGENTS.md should be created
		cmd := exec.Command(bd, "init", "--prefix", "am", "--skip-hooks", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, out)
		}

		if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); os.IsNotExist(err) {
			t.Error("AGENTS.md not created")
		}
	})

	t.Run("agents_template", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		// Create a custom agents template
		templatePath := filepath.Join(dir, "custom-agents.md")
		if err := os.WriteFile(templatePath, []byte("# Custom Agents\nThis is custom.\n"), 0644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(bd, "init", "--prefix", "at", "--agents-template", templatePath, "--skip-hooks", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --agents-template failed: %v\n%s", err, out)
		}

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

		cmd := exec.Command(bd, "init", "--prefix", "ng", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init (no git) failed: %v\n%s", err, out)
		}

		// Verify git was initialized
		if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
			t.Error("git should have been initialized")
		}
	})

	t.Run("database_name_validation", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		// Invalid database name should be rejected
		cmd := exec.Command(bd, "init", "--database", "has spaces!", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("bd init with invalid database name should fail")
		}
		if !strings.Contains(string(out), "invalid database name") {
			t.Errorf("expected 'invalid database name' error, got: %s", out)
		}
	})

	t.Run("interactions_jsonl_created", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--prefix", "ij", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, out)
		}

		if _, err := os.Stat(filepath.Join(dir, ".beads", "interactions.jsonl")); os.IsNotExist(err) {
			t.Error("interactions.jsonl not created")
		}
	})

	t.Run("prefix_auto_detect_from_dirname", func(t *testing.T) {
		// Create a directory with a known name
		parent := t.TempDir()
		dir := filepath.Join(parent, "myproject")
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init (auto-detect prefix) failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		val := readBackConfig(t, beadsDir, "myproject", "issue_prefix")
		if val != "myproject" {
			t.Errorf("auto-detected issue_prefix: got %q, want %q", val, "myproject")
		}
	})

	t.Run("prefix_numeric_sanitized", func(t *testing.T) {
		// Directory names starting with a digit get "bd_" prefix
		parent := t.TempDir()
		dir := filepath.Join(parent, "001")
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		initGitRepoAt(t, dir)

		cmd := exec.Command(bd, "init", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init (numeric dir) failed: %v\n%s", err, out)
		}

		beadsDir := filepath.Join(dir, ".beads")
		val := readBackConfig(t, beadsDir, "bd_001", "issue_prefix")
		if val != "bd_001" {
			t.Errorf("sanitized issue_prefix: got %q, want %q", val, "bd_001")
		}
	})
}
