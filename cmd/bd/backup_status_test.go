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
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
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

// TestBackupStatusListsEveryBackupDestination pins GH#5209: a destination
// `bd backup init` did not register (auto-backup's backup_export, or one
// added by hand) is listed, not reported as no backup at all.
func TestBackupStatusListsEveryBackupDestination(t *testing.T) {
	prepareBackupStatusTest(t)
	sizeDatabase := func(context.Context) (int64, bool, error) {
		return 0, false, nil
	}
	listBackups := func(context.Context) ([]storage.BackupDestination, bool, error) {
		return []storage.BackupDestination{{Name: "backup_export", URL: "file:///repo/.beads/backup"}}, true, nil
	}

	t.Run("json", func(t *testing.T) {
		cmd := newBackupStatusTestRootWithBackups(sizeDatabase, listBackups)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		var response struct {
			Dolt struct {
				Configured   bool                        `json:"configured"`
				Destinations []storage.BackupDestination `json:"destinations"`
			} `json:"dolt"`
		}
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
		}
		if len(response.Dolt.Destinations) != 1 || response.Dolt.Destinations[0].Name != "backup_export" ||
			response.Dolt.Destinations[0].URL != "file:///repo/.beads/backup" {
			t.Errorf("dolt.destinations = %+v, want backup_export: %s", response.Dolt.Destinations, stdout)
		}
		if response.Dolt.Configured {
			t.Errorf("dolt.configured = true, want false: no bd backup init destination: %s", stdout)
		}
	})

	t.Run("human", func(t *testing.T) {
		cmd := newBackupStatusTestRootWithBackups(sizeDatabase, listBackups)
		cmd.SetArgs([]string{"backup", "status"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if strings.Contains(stdout, "No backup has been performed yet.") {
			t.Errorf("status claims no backup while one is registered: %s", stdout)
		}
		if !strings.Contains(stdout, "  backup_export: file:///repo/.beads/backup") {
			t.Errorf("registered destination missing: %s", stdout)
		}
	})

	t.Run("json lists none as empty", func(t *testing.T) {
		none := func(context.Context) ([]storage.BackupDestination, bool, error) { return nil, true, nil }
		cmd := newBackupStatusTestRootWithBackups(sizeDatabase, none)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		if !strings.Contains(stdout, `"destinations": []`) {
			t.Errorf("want an empty destinations list: %s", stdout)
		}
	})

	t.Run("list error", func(t *testing.T) {
		failing := func(context.Context) ([]storage.BackupDestination, bool, error) {
			return nil, false, errors.New("dolt_backups unreadable")
		}
		cmd := newBackupStatusTestRootWithBackups(sizeDatabase, failing)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		assertExitCode(t, err, 1)
		if !strings.Contains(stdout, "list backup destinations: dolt_backups unreadable") {
			t.Errorf("want the list error reported: %s", stdout)
		}
	})
}

// TestBackupStatusUnlistedOutputIsByteIdentical pins the no-change side of
// GH#5209: when the backend cannot list destinations, status prints exactly
// the bytes it printed before the listing existed, in text and in --json.
// The text output is also unchanged when the listing is available but empty.
func TestBackupStatusUnlistedOutputIsByteIdentical(t *testing.T) {
	const wantHuman = "No backup has been performed yet.\n" +
		"\n" +
		"Setup:\n" +
		"  bd backup init <path>    Set up a backup destination\n" +
		"  bd backup sync           Push to backup destination\n"
	const wantJSON = "{\n" +
		"  \"backup\": {\n" +
		"    \"last_dolt_commit\": \"\",\n" +
		"    \"timestamp\": \"0001-01-01T00:00:00Z\",\n" +
		"    \"last_cap_warn_at\": \"0001-01-01T00:00:00Z\"\n" +
		"  },\n" +
		"  \"dolt\": {\n" +
		"    \"configured\": false\n" +
		"  },\n" +
		"  \"size_cap\": {\n" +
		"    \"cap_mb\": 2048,\n" +
		"    \"current_bytes\": 0,\n" +
		"    \"enabled\": true,\n" +
		"    \"exceeded\": false\n" +
		"  }\n" +
		"}\n"
	sizeDatabase := func(context.Context) (int64, bool, error) { return 0, false, nil }
	unlisted := func(context.Context) ([]storage.BackupDestination, bool, error) { return nil, false, nil }
	listedEmpty := func(context.Context) ([]storage.BackupDestination, bool, error) { return nil, true, nil }

	for _, tc := range []struct {
		name        string
		listBackups backupListFunc
		args        []string
		want        string
	}{
		{"human unlisted", unlisted, []string{"backup", "status"}, wantHuman},
		{"human listed empty", listedEmpty, []string{"backup", "status"}, wantHuman},
		{"json unlisted", unlisted, []string{"backup", "status", "--json"}, wantJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareBackupStatusTest(t)
			cmd := newBackupStatusTestRootWithBackups(sizeDatabase, tc.listBackups)
			cmd.SetArgs(tc.args)
			stdout, stderr, err := executeBackupStatusCommand(t, cmd)
			if err != nil {
				t.Fatalf("backup status: %v", err)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			if stdout != tc.want {
				t.Errorf("stdout differs from the pre-listing output\n got: %q\nwant: %q", stdout, tc.want)
			}
		})
	}
}

// TestBackupStatusListingLeavesBackupStateUnchanged pins the invariant side
// of GH#5209: listing destinations is read-only reporting. Against the same
// seeded state, status with and without a listed destination reports the
// same backup state, the same configured identity and the same size cap,
// byte for byte, and leaves backup_state.json byte-identical on disk.
func TestBackupStatusListingLeavesBackupStateUnchanged(t *testing.T) {
	prepareBackupStatusTest(t)
	sizeDatabase := func(context.Context) (int64, bool, error) { return 0, false, nil }
	unlisted := func(context.Context) ([]storage.BackupDestination, bool, error) { return nil, false, nil }
	listed := func(context.Context) ([]storage.BackupDestination, bool, error) {
		return []storage.BackupDestination{{Name: "backup_export", URL: "file:///repo/.beads/backup"}}, true, nil
	}

	dir, err := backupDir()
	if err != nil {
		t.Fatalf("backupDir: %v", err)
	}
	seeded := &backupState{Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), LastDoltCommit: "deadbeef"}
	if err := saveBackupState(dir, seeded); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "backup_state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read seeded state: %v", err)
	}

	status := func(listBackups backupListFunc) map[string]json.RawMessage {
		t.Helper()
		cmd := newBackupStatusTestRootWithBackups(sizeDatabase, listBackups)
		cmd.SetArgs([]string{"backup", "status", "--json"})
		stdout, _, err := executeBackupStatusCommand(t, cmd)
		if err != nil {
			t.Fatalf("backup status: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
		}
		return raw
	}
	without, with := status(unlisted), status(listed)

	for _, key := range []string{"backup", "size_cap"} {
		if !bytes.Equal(without[key], with[key]) {
			t.Errorf("%s changed when destinations were listed\nwithout: %s\n   with: %s", key, without[key], with[key])
		}
	}
	var doltWithout, doltWith map[string]json.RawMessage
	if err := json.Unmarshal(without["dolt"], &doltWithout); err != nil {
		t.Fatalf("dolt (unlisted): %v", err)
	}
	if err := json.Unmarshal(with["dolt"], &doltWith); err != nil {
		t.Fatalf("dolt (listed): %v", err)
	}
	if _, ok := doltWith["destinations"]; !ok {
		t.Fatalf("listed status has no dolt.destinations: %s", with["dolt"])
	}
	delete(doltWith, "destinations")
	if len(doltWith) != len(doltWithout) {
		t.Errorf("dolt keys changed beyond destinations\nwithout: %s\n   with: %s", without["dolt"], with["dolt"])
	}
	for key, v := range doltWithout {
		if !bytes.Equal(v, doltWith[key]) {
			t.Errorf("dolt.%s changed when destinations were listed: %s -> %s", key, v, doltWith[key])
		}
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after status: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("backup_state.json changed on disk\nbefore: %s\n after: %s", before, after)
	}
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
	noBackups := func(context.Context) ([]storage.BackupDestination, bool, error) {
		return nil, false, nil
	}
	return newBackupStatusTestRootWithBackups(sizeDatabase, noBackups)
}

func newBackupStatusTestRootWithBackups(sizeDatabase backupSizeFunc, listBackups backupListFunc) *cobra.Command {
	root := &cobra.Command{Use: "bd"}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output JSON")
	backup := &cobra.Command{Use: "backup"}
	backup.AddCommand(newBackupStatusCommand(sizeDatabase, listBackups))
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
