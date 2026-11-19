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
	"github.com/steveyegge/beads/internal/configfile"
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

// seedBackupStateJSON writes a raw backup_state.json into the workspace's
// backup directory and returns that directory. Seeding raw JSON (not
// saveBackupState) keeps tests for GH#4070 (bd-5mwx39) compiling before the
// failure fields exist and lets them hold states an older bd wrote.
func seedBackupStateJSON(t *testing.T, beadsDir, raw string) string {
	t.Helper()
	dir := filepath.Join(beadsDir, "backup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup_state.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBackupStatusReportsFailedAutoBackup is the reporting half of GH#4070
// (bd-5mwx39): once failed syncs are recorded, `bd backup status` must show
// the last SUCCESS as "Last backup", name the failed attempt as FAILED with
// its error, and point at the move-aside recovery — in text and in --json —
// instead of advertising the throttle watermark of a backup that never
// happened.
func TestBackupStatusReportsFailedAutoBackup(t *testing.T) {
	const failureText = "sync to backup: sync backup backup_export: Error 1105: error opening table file: table file not found: /backup/0123456789"
	// Whole-second times: status prints them back through time.RFC3339.
	t1 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	t2 := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	t3 := time.Now().UTC().Add(-25 * time.Minute).Truncate(time.Second)
	seeded := fmt.Sprintf(`{
  "last_dolt_commit": "deadbeef",
  "timestamp": %q,
  "last_cap_warn_at": %q,
  "last_success_at": %q,
  "last_error": %q,
  "last_error_at": %q
}`,
		t2.Format(time.RFC3339), t3.Format(time.RFC3339), t1.Format(time.RFC3339),
		failureText, t2.Format(time.RFC3339))

	sizeDatabase := func(context.Context) (int64, bool, error) { return 0, false, nil }

	t.Run("text_shows_last_success_and_failed_attempt", func(t *testing.T) {
		beadsDir := prepareBackupStatusTest(t)
		dir := seedBackupStateJSON(t, beadsDir, seeded)

		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, stderr, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(stdout, "Last backup: "+t1.Format(time.RFC3339)) {
			t.Errorf("status does not show the last successful backup %s:\n%s", t1.Format(time.RFC3339), stdout)
		}
		if strings.Contains(stdout, "Last backup: "+t2.Format(time.RFC3339)) {
			t.Errorf("status advertises the FAILED attempt's time %s as the last backup:\n%s", t2.Format(time.RFC3339), stdout)
		}
		if !strings.Contains(stdout, "Last attempt: FAILED "+t2.Format(time.RFC3339)) {
			t.Errorf("status does not show the failed attempt %s as FAILED:\n%s", t2.Format(time.RFC3339), stdout)
		}
		if !strings.Contains(stdout, failureText) {
			t.Errorf("status does not print the failure's error text:\n%s", stdout)
		}
		if !strings.Contains(stdout, dir) {
			t.Errorf("recovery hint does not name the backup directory %s:\n%s", dir, stdout)
		}
		if !strings.Contains(stdout, "mv "+dir) {
			t.Errorf("recovery hint does not spell the move-aside step:\n%s", stdout)
		}
	})

	t.Run("json_carries_success_failure_and_watermarks", func(t *testing.T) {
		beadsDir := prepareBackupStatusTest(t)
		seedBackupStateJSON(t, beadsDir, seeded)

		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status --json: %v", err)
		}
		var response struct {
			Backup struct {
				LastDoltCommit string    `json:"last_dolt_commit"`
				Timestamp      time.Time `json:"timestamp"`
				LastCapWarnAt  time.Time `json:"last_cap_warn_at"`
				LastSuccessAt  time.Time `json:"last_success_at"`
				LastError      string    `json:"last_error"`
				LastErrorAt    time.Time `json:"last_error_at"`
			} `json:"backup"`
		}
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("parse backup status JSON (%v):\n%s", err, stdout)
		}
		b := response.Backup
		if !b.LastSuccessAt.Equal(t1) {
			t.Errorf("backup.last_success_at = %v, want the last success %v", b.LastSuccessAt, t1)
		}
		if b.LastError != failureText {
			t.Errorf("backup.last_error = %q, want %q", b.LastError, failureText)
		}
		if !b.LastErrorAt.Equal(t2) {
			t.Errorf("backup.last_error_at = %v, want %v", b.LastErrorAt, t2)
		}
		if b.LastDoltCommit != "deadbeef" {
			t.Errorf("backup.last_dolt_commit = %q, want deadbeef", b.LastDoltCommit)
		}
		if !b.Timestamp.Equal(t2) {
			t.Errorf("backup.timestamp = %v, want the failed attempt's watermark %v", b.Timestamp, t2)
		}
		if !b.LastCapWarnAt.Equal(t3) {
			t.Errorf("backup.last_cap_warn_at = %v, want %v", b.LastCapWarnAt, t3)
		}
	})

	t.Run("legacy_state_prints_its_watermark_unchanged", func(t *testing.T) {
		t0 := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
		beadsDir := prepareBackupStatusTest(t)
		seedBackupStateJSON(t, beadsDir, fmt.Sprintf(`{
  "last_dolt_commit": "deadbeef",
  "timestamp": %q
}`, t0.Format(time.RFC3339)))

		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if !strings.Contains(stdout, "Last backup: "+t0.Format(time.RFC3339)) {
			t.Errorf("legacy state no longer prints its watermark as the last backup:\n%s", stdout)
		}
		if strings.Contains(stdout, "FAILED") || strings.Contains(stdout, "none succeeded") {
			t.Errorf("legacy state with no recorded failure prints a failure block:\n%s", stdout)
		}
	})

	t.Run("failure_without_success_says_none_succeeded", func(t *testing.T) {
		beadsDir := prepareBackupStatusTest(t)
		dir := seedBackupStateJSON(t, beadsDir, fmt.Sprintf(`{
  "timestamp": %q,
  "last_error": %q,
  "last_error_at": %q
}`, t2.Format(time.RFC3339), failureText, t2.Format(time.RFC3339)))

		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if !strings.Contains(stdout, "Last backup: none succeeded yet") {
			t.Errorf("a state with only failed attempts does not say none succeeded yet:\n%s", stdout)
		}
		if !strings.Contains(stdout, "Last attempt: FAILED "+t2.Format(time.RFC3339)) {
			t.Errorf("the failed attempt is not shown as FAILED:\n%s", stdout)
		}
		if !strings.Contains(stdout, "mv "+dir) {
			t.Errorf("recovery hint does not spell the move-aside step:\n%s", stdout)
		}
	})

	t.Run("no_failure_prints_no_failed_line", func(t *testing.T) {
		beadsDir := prepareBackupStatusTest(t)
		seedBackupStateJSON(t, beadsDir, fmt.Sprintf(`{
  "last_dolt_commit": "deadbeef",
  "timestamp": %q,
  "last_success_at": %q
}`, t2.Format(time.RFC3339), t1.Format(time.RFC3339)))

		cmd := newBackupStatusTestRoot(sizeDatabase)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if !strings.Contains(stdout, "Last backup: "+t1.Format(time.RFC3339)) {
			t.Errorf("status does not show the last successful backup:\n%s", stdout)
		}
		if strings.Contains(stdout, "FAILED") {
			t.Errorf("a healthy state prints a FAILED line:\n%s", stdout)
		}
	})
}
