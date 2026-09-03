//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type lastTouchedProcessResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

func runLastTouchedProcess(t *testing.T, binPath, workDir string, args ...string) lastTouchedProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	profileDir := t.TempDir()
	env := make([]string, 0, len(os.Environ())+12)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		lowerKey := strings.ToLower(key)
		if strings.HasPrefix(lowerKey, "bd_") || strings.HasPrefix(lowerKey, "beads_") {
			continue
		}
		switch lowerKey {
		case "home", "userprofile", "appdata", "localappdata", "xdg_config_home":
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"HOME="+profileDir,
		"USERPROFILE="+profileDir,
		"APPDATA="+filepath.Join(profileDir, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(profileDir, "AppData", "Local"),
		"XDG_CONFIG_HOME="+filepath.Join(profileDir, "xdg-config"),
		"BEADS_TEST_MODE=1",
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_TEST_CIRCUIT_DIR="+filepath.Join(profileDir, "circuit"),
		"BEADS_NO_DAEMON=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BD_NON_INTERACTIVE=1",
		"LINEAR_API_KEY=",
		"NO_COLOR=1",
	)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("bd %v timed out after 30s\nstdout: %s\nstderr: %s", args, stdout.String(), stderr.String())
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("bd %v in %s: %v\nstdout: %s\nstderr: %s", args, workDir, err, stdout.String(), stderr.String())
		}
		exitCode = exitErr.ExitCode()
	}
	return lastTouchedProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

func assertSuccessfulJSONShow(t *testing.T, result lastTouchedProcessResult, wantID string) {
	t.Helper()
	if result.exitCode != 0 {
		t.Fatalf("show exit code = %d, want 0\nstdout: %s\nstderr: %s", result.exitCode, result.stdout, result.stderr)
	}
	var issues []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result.stdout, &issues); err != nil {
		t.Fatalf("show stdout is not valid JSON: %v\nstdout: %s\nstderr: %s", err, result.stdout, result.stderr)
	}
	if len(issues) != 1 || issues[0].ID != wantID {
		t.Fatalf("show JSON IDs = %+v, want exactly %q", issues, wantID)
	}
}

func setLastTouchedSentinel(t *testing.T, path string, contents []byte) time.Time {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write last-touched sentinel: %v", err)
	}
	fixed := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatalf("set last-touched sentinel mtime: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat last-touched sentinel: %v", err)
	}
	return info.ModTime()
}

func assertLastTouchedUnchanged(t *testing.T, path string, wantContents []byte, wantMtime time.Time) {
	t.Helper()
	gotContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read last-touched after readonly show: %v", err)
	}
	if !bytes.Equal(gotContents, wantContents) {
		t.Fatalf("last-touched bytes changed after readonly show: got %q, want %q", gotContents, wantContents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat last-touched after readonly show: %v", err)
	}
	if !info.ModTime().Equal(wantMtime) {
		t.Fatalf("last-touched mtime changed after readonly show: got %v, want %v", info.ModTime(), wantMtime)
	}
}

// TestReadonlyShowPreservesLastTouched covers strict --readonly at the process
// boundary. A successful JSON read must preserve an absent marker and an
// existing marker byte-for-byte (including mtime). Ordinary show remains
// responsible for advancing the marker mtime even when it rewrites the same
// issue ID.
func TestReadonlyShowPreservesLastTouched(t *testing.T) {
	binPath := buildBDUnderTest(t)
	workDir := t.TempDir()
	initBeadsWorkspace(t, binPath, workDir)

	id := runBDStdout(t, binPath, workDir, "q", "readonly last-touched fixture")
	if id == "" {
		t.Fatal("bd q returned empty issue ID")
	}
	lastTouchedPath := filepath.Join(workDir, ".beads", lastTouchedFile)

	t.Run("preserves absent marker", func(t *testing.T) {
		if err := os.Remove(lastTouchedPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove last-touched setup marker: %v", err)
		}
		result := runLastTouchedProcess(t, binPath, workDir, "--readonly", "show", id, "--json")
		assertSuccessfulJSONShow(t, result, id)
		if _, err := os.Stat(lastTouchedPath); !os.IsNotExist(err) {
			t.Fatalf("readonly show created absent last-touched marker: stat error = %v", err)
		}
	})

	sentinel := []byte("sentinel\r\nbytes\x00")
	t.Run("preserves sentinel bytes and mtime", func(t *testing.T) {
		mtime := setLastTouchedSentinel(t, lastTouchedPath, sentinel)
		result := runLastTouchedProcess(t, binPath, workDir, "--readonly", "show", id, "--json")
		assertSuccessfulJSONShow(t, result, id)
		assertLastTouchedUnchanged(t, lastTouchedPath, sentinel, mtime)
	})

	t.Run("ordinary JSON show still advances marker mtime", func(t *testing.T) {
		contents := []byte(id + "\n")
		mtime := setLastTouchedSentinel(t, lastTouchedPath, contents)
		result := runLastTouchedProcess(t, binPath, workDir, "show", id, "--json")
		assertSuccessfulJSONShow(t, result, id)

		gotContents, err := os.ReadFile(lastTouchedPath)
		if err != nil {
			t.Fatalf("read last-touched after ordinary show: %v", err)
		}
		if !bytes.Equal(gotContents, contents) {
			t.Fatalf("ordinary show last-touched bytes = %q, want %q", gotContents, contents)
		}
		info, err := os.Stat(lastTouchedPath)
		if err != nil {
			t.Fatalf("stat last-touched after ordinary show: %v", err)
		}
		if !info.ModTime().After(mtime) {
			t.Fatalf("ordinary show did not advance last-touched mtime: got %v, previous %v", info.ModTime(), mtime)
		}
	})
}
