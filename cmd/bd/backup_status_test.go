package main

import (
	"context"
	"encoding/json"
	"errors"
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

// seedFailedAutoBackupState writes a backup_state.json, as raw JSON so the
// fixture does not depend on backupState's Go fields, for a workspace whose
// last good backup ran at t1 and whose later attempt at t2 failed with
// errText. It returns the backup directory.
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
}
