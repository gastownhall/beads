package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

func TestBackupStatusProxiedGuardDoesNotMeasureSize(t *testing.T) {
	prepareBackupStatusTest(t)
	proxiedServerMode = true
	called := false
	sizeDatabase := func(context.Context) (int64, bool, error) {
		called = true
		return 1, true, nil
	}

	cmd := newBackupStatusTestRoot(sizeDatabase)
	cmd.SetArgs([]string{"backup", "status"})
	_, _, err := executeBackupStatusCommand(t, cmd)
	assertExitCode(t, err, 1)
	if called {
		t.Fatal("proxied-server guard called database size provider")
	}
}

func prepareBackupStatusTest(t *testing.T) {
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
