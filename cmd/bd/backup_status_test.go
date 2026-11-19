package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestBackupStatusSizeErrorRespectsOutputMode(t *testing.T) {
	prepareBackupStatusTest(t)
	missingRoot := filepath.Join(t.TempDir(), "missing")
	sizeErr := errors.New("active database directory is missing: " + missingRoot)
	sizeDatabase := func(context.Context) (int64, bool, error) {
		return 0, false, sizeErr
	}

	t.Run("json", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status", "--json"})

		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		assertExitCode(t, err, 1)
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}

		var response struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("stdout is not a single valid JSON response: %v\nstdout: %s", err, stdout)
		}
		if !strings.Contains(response.Error, "measure database size") || !strings.Contains(response.Error, missingRoot) {
			t.Errorf("error = %q, want database-size diagnostic containing %q", response.Error, missingRoot)
		}
		if strings.Contains(stdout, "Usage:") {
			t.Errorf("stdout contains usage noise: %s", stdout)
		}
	})

	t.Run("human", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})

		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		assertExitCode(t, err, 1)
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
		if !strings.Contains(stderr, "Error: measure database size:") || !strings.Contains(stderr, missingRoot) {
			t.Errorf("stderr = %q, want database-size diagnostic containing %q", stderr, missingRoot)
		}
		if strings.Contains(stderr, "Usage:") || strings.Contains(stderr, "exit code 1") {
			t.Errorf("stderr contains Cobra noise: %s", stderr)
		}
	})
}

func TestBackupStatusOmitsUnsupportedDatabaseSize(t *testing.T) {
	prepareBackupStatusTest(t)
	sizeDatabase := func(context.Context) (int64, bool, error) {
		return 0, false, nil
	}

	t.Run("json", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status", "--json"})

		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		var response map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
		}
		if _, ok := response["database_size"]; ok {
			t.Errorf("database_size should be omitted: %s", stdout)
		}
		if _, ok := response["backup"]; !ok {
			t.Errorf("backup status missing backup field: %s", stdout)
		}
		if _, ok := response["dolt"]; !ok {
			t.Errorf("backup status missing dolt field: %s", stdout)
		}
	})

	t.Run("human", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})

		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		if strings.Contains(stdout, "Database size:") {
			t.Errorf("unsupported database size should be omitted: %s", stdout)
		}
		if !strings.Contains(stdout, "No backup has been performed yet.") {
			t.Errorf("remaining status output was not rendered: %s", stdout)
		}
	})
}

func TestBackupStatusIncludesAvailableDatabaseSize(t *testing.T) {
	prepareBackupStatusTest(t)
	sizeDatabase := func(context.Context) (int64, bool, error) {
		return 1536, true, nil
	}

	t.Run("json", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		var response struct {
			DatabaseSize struct {
				Bytes int64 `json:"bytes"`
			} `json:"database_size"`
		}
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
		}
		if response.DatabaseSize.Bytes != 1536 {
			t.Errorf("database_size.bytes = %d, want 1536", response.DatabaseSize.Bytes)
		}
	})

	t.Run("human", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		if !strings.Contains(stdout, "Database size: 1.5 KB") {
			t.Errorf("available database size missing: %s", stdout)
		}
	})
}

// TestBackupStatusShowsSizeCapPaused pins the PR #6071 review's second
// major point: `bd --json` / `--quiet` callers never saw the size-cap
// pause — the warning is stderr-only and throttled, and status previously
// said nothing about the cap, so an agent/CI caller saw a reassuring
// "Last backup" line while auto-backup was silently, permanently paused.
func TestBackupStatusShowsSizeCapPaused(t *testing.T) {
	t.Setenv("BD_BACKUP_SIZE_CAP_MB", "1")
	prepareBackupStatusTest(t)
	sizeDatabase := func(context.Context) (int64, bool, error) { return 0, false, nil }

	dir, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "filler"), make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	seeded := &backupState{Timestamp: time.Now().UTC().Add(-time.Hour), LastDoltCommit: "deadbeef"}
	if err := saveBackupState(dir, seeded); err != nil {
		t.Fatal(err)
	}

	t.Run("json", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		var response struct {
			SizeCap struct {
				Enabled      bool  `json:"enabled"`
				CapMB        int   `json:"cap_mb"`
				CurrentBytes int64 `json:"current_bytes"`
				Exceeded     bool  `json:"exceeded"`
			} `json:"size_cap"`
		}
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
		}
		if !response.SizeCap.Enabled {
			t.Errorf("size_cap.enabled = false, want true: %s", stdout)
		}
		if !response.SizeCap.Exceeded {
			t.Errorf("size_cap.exceeded = false, want true: %s", stdout)
		}
		if response.SizeCap.CapMB != 1 {
			t.Errorf("size_cap.cap_mb = %d, want 1: %s", response.SizeCap.CapMB, stdout)
		}
		if response.SizeCap.CurrentBytes < 2*1024*1024 {
			t.Errorf("size_cap.current_bytes = %d, want >= 2MB: %s", response.SizeCap.CurrentBytes, stdout)
		}
	})

	t.Run("human", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		var pausedLine string
		for _, line := range strings.Split(stdout, "\n") {
			if strings.Contains(line, "PAUSED (cap exceeded)") {
				pausedLine = line
			}
		}
		if pausedLine == "" {
			t.Fatalf("status text missing PAUSED indicator: %s", stdout)
		}
		// Same levers as the pause warning — see
		// TestPauseAutoBackupForSizeCap_RemediationAdvice.
		for _, want := range []string{"backup.size-cap-mb", "backup.git-repo"} {
			if !strings.Contains(pausedLine, want) {
				t.Errorf("PAUSED line missing %s pointer: %q", want, pausedLine)
			}
		}
		if strings.Contains(pausedLine, "bd backup init") {
			t.Errorf("PAUSED line points at `bd backup init`, which does not move the auto-backup destination: %q", pausedLine)
		}
	})
}

// TestBackupStatusShowsSizeCapDisabled pins the PR #6071 review's first
// major point from the status side: backup.size-cap-mb: 0 must render as
// disabled in status, not silently report the legacy 2048MB default.
func TestBackupStatusShowsSizeCapDisabled(t *testing.T) {
	t.Setenv("BD_BACKUP_SIZE_CAP_MB", "0")
	prepareBackupStatusTest(t)
	sizeDatabase := func(context.Context) (int64, bool, error) { return 0, false, nil }

	dir, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir: %v", err)
	}
	seeded := &backupState{Timestamp: time.Now().UTC().Add(-time.Hour), LastDoltCommit: "deadbeef"}
	if err := saveBackupState(dir, seeded); err != nil {
		t.Fatal(err)
	}

	t.Run("json", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
		}
		sizeCapRaw, ok := raw["size_cap"]
		if !ok {
			t.Fatalf("size_cap key missing from status JSON: %s", stdout)
		}
		var sizeCap struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(sizeCapRaw, &sizeCap); err != nil {
			t.Fatalf("size_cap is not valid JSON: %v\nraw: %s", err, sizeCapRaw)
		}
		if sizeCap.Enabled {
			t.Errorf("size_cap.enabled = true with backup.size-cap-mb=0, want false: %s", stdout)
		}
	})

	t.Run("human", func(t *testing.T) {
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if !strings.Contains(stdout, "Size cap: disabled") {
			t.Errorf("status text missing disabled size-cap line: %s", stdout)
		}
	})
}

// TestBackupStatusProxiedGuardFollowsLocality replaces a test that asserted a
// blanket proxied refusal. Since slice S3 the guard is scoped to locality: bd
// owns the dolt process on a managed-local workspace and can honor the command
// there, while an external one is still refused — and a refused command must
// not go on to measure anything, because the directory it would measure belongs
// to a server on another host.
func TestBackupStatusProxiedGuardFollowsLocality(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sidecar     string
		wantMeasure bool
	}{
		{
			name:        "external topology is refused without measuring",
			sidecar:     `{"external":{"host":"db.example.com","port":3306}}`,
			wantMeasure: false,
		},
		{
			name:        "managed local is honored and measures",
			sidecar:     `{}`,
			wantMeasure: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := prepareBackupStatusTest(t)
			proxiedServerMode = true
			if err := os.WriteFile(configfile.ProxiedServerClientInfoPath(beadsDir), []byte(tc.sidecar), 0o600); err != nil {
				t.Fatalf("write proxied sidecar: %v", err)
			}

			measured := false
			sizeDatabase := func(context.Context) (int64, bool, error) {
				measured = true
				return 1, true, nil
			}

			cmd := newBackupStatusTestRoot(sizeDatabase)
			cmd.SetArgs([]string{"backup", "status"})
			_, _, err := executeBackupStatusCommand(t, cmd)
			if tc.wantMeasure {
				if err != nil {
					t.Fatalf("backup status on managed-local: %v", err)
				}
			} else {
				assertExitCode(t, err, 1)
			}
			if measured != tc.wantMeasure {
				t.Fatalf("size provider called = %v, want %v", measured, tc.wantMeasure)
			}
		})
	}
}

// prepareBackupStatusTest builds a throwaway workspace and returns its .beads
// directory, so a caller can drop workspace files (the proxied sidecar) into it.
func prepareBackupStatusTest(t *testing.T) string {
	t.Helper()

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
		t.Fatalf("create workspace marker: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_JSON_ENVELOPE", "0")
	initConfigForTest(t)

	oldJSONOutput := jsonOutput
	oldProxiedServerMode := proxiedServerMode
	jsonOutput = false
	proxiedServerMode = false
	resetCommandContext()
	t.Cleanup(func() {
		jsonOutput = oldJSONOutput
		proxiedServerMode = oldProxiedServerMode
		resetCommandContext()
	})
	return beadsDir
}

func newBackupStatusTestRoot(sizeDatabase backupSizeFunc) *cobra.Command {
	root := &cobra.Command{Use: "bd"}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output JSON")
	backup := &cobra.Command{Use: "backup"}
	backup.AddCommand(newBackupStatusCommand(sizeDatabase))
	root.AddCommand(backup)
	return root
}

func executeBackupStatusCommand(t *testing.T, cmd *cobra.Command) (string, string, error) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	captureDir := t.TempDir()
	stdoutPath := filepath.Join(captureDir, "stdout")
	stderrPath := filepath.Join(captureDir, "stderr")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		_ = stdoutFile.Close()
		t.Fatalf("create stderr capture: %v", err)
	}

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	var commandErr error
	func() {
		defer func() {
			os.Stdout = oldStdout
			os.Stderr = oldStderr
		}()
		os.Stdout = stdoutFile
		os.Stderr = stderrFile
		commandErr = cmd.Execute()
	}()

	if err := stdoutFile.Close(); err != nil {
		t.Fatalf("close stdout capture: %v", err)
	}
	if err := stderrFile.Close(); err != nil {
		t.Fatalf("close stderr capture: %v", err)
	}
	stdout, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	stderr, err := os.ReadFile(stderrPath)
	if err != nil {
		t.Fatalf("read stderr capture: %v", err)
	}
	return string(stdout), string(stderr), commandErr
}

func assertExitCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("command succeeded, want exit code %d", want)
	}
	got, ok := exitCodeFromError(err)
	if !ok || got != want {
		t.Fatalf("command error = %v, want exit code %d", err, want)
	}
}

// seedFailedAutoBackupState writes a backup_state.json, as raw JSON so the
// fixture does not depend on backupState's Go fields, for a workspace whose
// last good backup ran at t1 and whose later attempt at t2 failed with
// errText; a zero t1 is a success an older bd made without recording its
// time. It returns the backup directory.
func seedFailedAutoBackupState(t *testing.T, t1, t2, capWarn time.Time, errText string) string {
	t.Helper()
	dir, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir: %v", err)
	}
	raw := map[string]any{
		"last_dolt_commit": "deadbeef",
		"timestamp":        t2,
		"last_cap_warn_at": capWarn,
		"last_success_at":  t1,
		"last_error":       errText,
		"last_error_at":    t2,
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup_state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBackupStatusReportsFailedAutoBackup pins bd-5mwx39 (GH#4070): a backup
// destination that fails on every attempt must not read as backed up. Each
// failed attempt advances the throttle timestamp, so status has to report the
// last success and the last failure separately.
func TestBackupStatusReportsFailedAutoBackup(t *testing.T) {
	sizeDatabase := func(context.Context) (int64, bool, error) { return 0, false, nil }
	t1 := time.Date(2026, 5, 5, 18, 31, 51, 0, time.UTC)
	t2 := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	capWarn := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	const errText = "sync to backup: sync backup backup_export: Error 1105: error opening table file: table file not found: boom"

	t.Run("json", func(t *testing.T) {
		prepareBackupStatusTest(t)
		seedFailedAutoBackupState(t, t1, t2, capWarn, errText)

		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status --json: %v\nstderr: %s", err, stderr)
		}
		var response struct {
			Backup map[string]json.RawMessage `json:"backup"`
		}
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
		}
		wantTime := func(key string, want time.Time) {
			t.Helper()
			raw, ok := response.Backup[key]
			if !ok {
				t.Errorf("backup.%s missing: %s", key, stdout)
				return
			}
			var got time.Time
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Errorf("backup.%s = %s, not a time: %v", key, raw, err)
				return
			}
			if !got.Equal(want) {
				t.Errorf("backup.%s = %v, want %v", key, got, want)
			}
		}
		wantString := func(key, want string) {
			t.Helper()
			raw, ok := response.Backup[key]
			if !ok {
				t.Errorf("backup.%s missing: %s", key, stdout)
				return
			}
			var got string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Errorf("backup.%s = %s, not a string: %v", key, raw, err)
				return
			}
			if got != want {
				t.Errorf("backup.%s = %q, want %q", key, got, want)
			}
		}
		wantTime("last_success_at", t1)
		wantString("last_error", errText)
		wantTime("last_error_at", t2)
		wantString("last_dolt_commit", "deadbeef")
		wantTime("timestamp", t2)
		wantTime("last_cap_warn_at", capWarn)
	})

	runText := func(t *testing.T) string {
		t.Helper()
		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v\nstderr: %s", err, stderr)
		}
		return stdout
	}
	lineWith := func(out, substr string) string {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, substr) {
				return line
			}
		}
		return ""
	}

	t.Run("text", func(t *testing.T) {
		prepareBackupStatusTest(t)
		dir := seedFailedAutoBackupState(t, t1, t2, capWarn, errText)

		out := runText(t)
		last := lineWith(out, "Last backup:")
		if !strings.Contains(last, t1.Format(time.RFC3339)) {
			t.Errorf("Last backup line = %q, want the last success %s\n%s", last, t1.Format(time.RFC3339), out)
		}
		if strings.Contains(last, t2.Format(time.RFC3339)) {
			t.Errorf("Last backup line shows the failed attempt %s: %q", t2.Format(time.RFC3339), last)
		}
		failed := lineWith(out, "FAILED")
		if failed == "" || !strings.Contains(failed, t2.Format(time.RFC3339)) {
			t.Errorf("no FAILED line carrying the attempt time %s:\n%s", t2.Format(time.RFC3339), out)
		}
		if !strings.Contains(out, errText) {
			t.Errorf("status does not show the error text %q:\n%s", errText, out)
		}
		src, dst := moveAsideHint(t, out)
		if src != dir {
			t.Errorf("move-aside hint moves %q, want the backup directory %s", src, dir)
		}
		if filepath.Dir(dst) != filepath.Dir(dir) || !strings.HasSuffix(dst, ".corrupt.backup") {
			t.Errorf("move-aside target = %q, want a *.corrupt.backup sibling of %s", dst, dir)
		}
	})

	t.Run("move_aside_target_is_gitignored", func(t *testing.T) {
		// The printed recovery must not leave a full copy of the database
		// for the project's next `git add -A`: the moved directory has to
		// be ignored by the .beads/.gitignore that bd init writes.
		beadsDir := prepareBackupStatusTest(t)
		root := filepath.Dir(beadsDir)
		initGitRepoAt(t, root)
		runGitForBootstrapTest(t, root, "config", "core.excludesFile", os.DevNull)
		if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte(doctor.GitignoreTemplate), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := seedFailedAutoBackupState(t, t1, t2, capWarn, errText)

		src, dst := moveAsideHint(t, runText(t))
		if src != dir {
			t.Fatalf("move-aside hint moves %q, want the backup directory %s", src, dir)
		}
		if err := os.Rename(src, dst); err != nil {
			t.Fatal(err)
		}
		// Control: a directory under any other name beside it is visible.
		control := filepath.Join(beadsDir, "backup.moved")
		if err := os.MkdirAll(control, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(control, "backup_state.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := gitCommand("status", "--porcelain", "--untracked-files=all")
		cmd.Dir = root
		cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
		status, err := cmd.Output()
		if err != nil {
			t.Fatalf("git status: %v", err)
		}
		if !strings.Contains(string(status), ".beads/backup.moved/") {
			t.Fatalf("control: git status does not list the un-ignored .beads/backup.moved/:\n%s", status)
		}
		if rel, _ := filepath.Rel(root, dst); strings.Contains(string(status), filepath.ToSlash(rel)) {
			t.Errorf("the moved-aside backup %s is not ignored by .beads/.gitignore:\n%s", rel, status)
		}
	})

	t.Run("git_repo_target_outside_the_repository", func(t *testing.T) {
		// Inside backup.git-repo no rule ignores the moved directory, so it
		// has to leave the repository.
		prepareBackupStatusTest(t)
		repo := filepath.Join(t.TempDir(), "backups")
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BD_BACKUP_GIT_REPO", repo)
		initConfigForTest(t)
		dir := seedFailedAutoBackupState(t, t1, t2, capWarn, errText)
		if want := filepath.Join(repo, "backup"); dir != want {
			t.Fatalf("precondition: backup directory = %s, want %s inside backup.git-repo", dir, want)
		}

		src, dst := moveAsideHint(t, runText(t))
		if src != dir {
			t.Errorf("move-aside hint moves %q, want the backup directory %s", src, dir)
		}
		rel, err := filepath.Rel(repo, dst)
		if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			t.Errorf("move-aside target %s is inside the backup repository %s", dst, repo)
		}
		if !strings.HasSuffix(dst, ".corrupt.backup") {
			t.Errorf("move-aside target = %q, want a *.corrupt.backup name", dst)
		}
	})

	t.Run("legacy_state_success_time_unknown", func(t *testing.T) {
		// What every release since v1.2.0 leaves after a failed sync: no
		// last_success_at, and a timestamp that failure advanced. Status
		// must not present that time as the last success.
		prepareBackupStatusTest(t)
		dir, err := backupDir()
		if err != nil {
			t.Fatalf("backupDir: %v", err)
		}
		data := fmt.Sprintf(`{"last_dolt_commit":"deadbeef","timestamp":%q,"last_cap_warn_at":"0001-01-01T00:00:00Z"}`, t2.Format(time.RFC3339))
		if err := os.WriteFile(filepath.Join(dir, "backup_state.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runText(t)
		last := lineWith(out, "Last backup:")
		if !strings.Contains(last, "unknown") || !strings.Contains(last, "at or before "+t2.Format(time.RFC3339)) {
			t.Errorf("Last backup line = %q, want an unknown time at or before the older bd's timestamp %s", last, t2.Format(time.RFC3339))
		}
		if strings.Contains(out, "FAILED") || strings.Contains(out, "mv ") {
			t.Errorf("legacy state with no recorded failure shows a failure:\n%s", out)
		}
	})

	t.Run("legacy_success_then_failure", func(t *testing.T) {
		// A failure this bd recorded over an older bd's state: a backup
		// succeeded once (last_dolt_commit), at a time nobody recorded.
		prepareBackupStatusTest(t)
		seedFailedAutoBackupState(t, time.Time{}, t2, capWarn, errText)

		out := runText(t)
		last := lineWith(out, "Last backup:")
		if !strings.Contains(last, "unknown") || strings.Contains(last, t2.Format(time.RFC3339)) {
			t.Errorf("Last backup line = %q, want an unknown time, not the failed attempt %s\n%s", last, t2.Format(time.RFC3339), out)
		}
		if failed := lineWith(out, "FAILED"); !strings.Contains(failed, t2.Format(time.RFC3339)) {
			t.Errorf("no FAILED line carrying the attempt time %s:\n%s", t2.Format(time.RFC3339), out)
		}
	})

	t.Run("failure_without_success", func(t *testing.T) {
		prepareBackupStatusTest(t)
		dir, err := backupDir()
		if err != nil {
			t.Fatalf("backupDir: %v", err)
		}
		data, err := json.Marshal(map[string]any{
			"last_dolt_commit": "",
			"timestamp":        t2,
			"last_cap_warn_at": time.Time{},
			"last_success_at":  time.Time{},
			"last_error":       errText,
			"last_error_at":    t2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "backup_state.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		out := runText(t)
		if strings.Contains(out, "No backup has been performed yet") {
			t.Errorf("a recorded failure reads as no attempt at all:\n%s", out)
		}
		if !strings.Contains(lineWith(out, "Last backup:"), "none succeeded yet") {
			t.Errorf("want \"Last backup: none succeeded yet\":\n%s", out)
		}
		if lineWith(out, "FAILED") == "" || !strings.Contains(out, errText) {
			t.Errorf("failure block missing:\n%s", out)
		}
	})

	t.Run("no_failure_no_hint", func(t *testing.T) {
		prepareBackupStatusTest(t)
		dir, err := backupDir()
		if err != nil {
			t.Fatalf("backupDir: %v", err)
		}
		data, err := json.Marshal(map[string]any{
			"last_dolt_commit": "deadbeef",
			"timestamp":        t1,
			"last_cap_warn_at": time.Time{},
			"last_success_at":  t1,
			"last_error":       "",
			"last_error_at":    time.Time{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "backup_state.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		out := runText(t)
		if last := lineWith(out, "Last backup:"); !strings.Contains(last, t1.Format(time.RFC3339)) {
			t.Errorf("Last backup line = %q, want %s", last, t1.Format(time.RFC3339))
		}
		if strings.Contains(out, "FAILED") || strings.Contains(out, "mv ") {
			t.Errorf("a healthy backup shows a failure or a recovery hint:\n%s", out)
		}
	})
}

// moveAsideHint returns the source and target of the `mv` line status prints
// under a failed attempt. Neither is quoted for a path free of shell
// metacharacters, which every test temp path is.
func moveAsideHint(t *testing.T, out string) (src, dst string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "mv" {
			if len(f) != 3 {
				t.Fatalf("move-aside hint %q is not `mv <dir> <target>`", line)
			}
			return f[1], f[2]
		}
	}
	t.Fatalf("no move-aside hint:\n%s", out)
	return "", ""
}

// TestBackupAsidePath pins where status tells the user to move a damaged
// auto-backup directory (bd-5mwx39): beside it under the *.corrupt.backup name
// that .beads/.gitignore ignores, or, for the backup/ directory inside
// backup.git-repo, beside that repository, where no commit to it can pick the
// copy up.
func TestBackupAsidePath(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 21, 0, 0, time.FixedZone("UTC+1", 3600))
	const suffix = ".20261119T082100Z.corrupt.backup"

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "backups")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(tmp, "project", ".beads")
	beadsBackup := filepath.Join(beadsDir, "backup")
	besideRepo := filepath.Join(tmp, "backups-backup"+suffix)

	for _, tc := range []struct {
		name, dir, gitRepo, want string
	}{
		{"beads_backup", beadsBackup, "", beadsBackup + suffix},
		{"git_repo_backup", filepath.Join(repo, "backup"), repo, besideRepo},
		{"git_repo_trailing_slash", filepath.Join(repo, "backup"), repo + string(filepath.Separator), besideRepo},
		// backupDir falls back to .beads/backup for a git-repo setting with
		// no .git, so a setting naming .beads itself is no repository.
		{"git_repo_without_dot_git", beadsBackup, beadsDir, beadsBackup + suffix},
		{"git_repo_set_but_not_used", beadsBackup, repo, beadsBackup + suffix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backupAsidePath(tc.dir, tc.gitRepo, now); got != tc.want {
				t.Errorf("backupAsidePath(%q, %q) = %q, want %q", tc.dir, tc.gitRepo, got, tc.want)
			}
		})
	}

	t.Run("relative_git_repo", func(t *testing.T) {
		t.Chdir(repo)
		if got := backupAsidePath("backup", ".", now); got != besideRepo {
			t.Errorf("backupAsidePath(%q, %q) = %q, want %q outside the repository", "backup", ".", got, besideRepo)
		}
	})
}
