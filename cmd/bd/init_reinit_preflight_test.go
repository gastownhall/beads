package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeReinitTargetMetadata writes metadata.json into beadsDir, creating it.
func writeReinitTargetMetadata(t *testing.T, beadsDir string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// requireExitCode fails unless err is an *exitError carrying want.
func requireExitCode(t *testing.T, err error, want int) {
	t.Helper()
	var exitErr *exitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exitError with code %d, got %v", want, err)
	}
	if exitErr.Code != want {
		t.Fatalf("want exit code %d, got %d", want, exitErr.Code)
	}
}

// TestRunInitReinitPreflight_CountErrorRefuses pins GH#5628 part 1: when the
// preflight cannot count the database it is about to destroy, --reinit-local
// must refuse rather than fall through to the destructive path with no count,
// no destroy token and no confirmation.
//
// BEADS_DIR names a different workspace than the one passed in, so the
// refusal must be about the argument (the gate's target), not about whatever
// BEADS_DIR or discovery would have found.
func TestRunInitReinitPreflight_CountErrorRefuses(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, "target", ".beads")
	// An unloadable metadata.json makes the store open (and so the count) fail.
	writeReinitTargetMetadata(t, beadsDir, []byte("{not json"))
	otherDir := filepath.Join(root, "other", ".beads")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", otherDir)

	if _, err := countReinitTargetIssues(beadsDir); err == nil {
		t.Fatal("precondition: countReinitTargetIssues should fail on an unloadable metadata.json")
	}

	var err error
	out := captureStderr(t, func() {
		err = runInitReinitPreflight(true, beadsDir, "bd", "")
	})
	if err == nil {
		t.Fatalf("runInitReinitPreflight returned nil after the count failed: the destroy confirmation was silently skipped\n%s", out)
	}
	requireExitCode(t, err, ExitDestroyTokenMissing)
	if !strings.Contains(out, "Target: "+beadsDir) {
		t.Errorf("warning must name the target %s, got:\n%s", beadsDir, out)
	}
	if strings.Contains(out, otherDir) {
		t.Errorf("warning names BEADS_DIR's workspace instead of the target, got:\n%s", out)
	}
	if !strings.Contains(out, "could not count them") {
		t.Errorf("warning must report the count error, got:\n%s", out)
	}
	if strings.Contains(out, "DESTROY-") {
		t.Errorf("refusal must never print the destroy token value (ADR-0002 Invariant 4), got:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "metadata.json")); statErr != nil {
		t.Errorf("the target must be left unchanged: %v", statErr)
	}
}

// TestConfirmReinitDestroy pins the confirmation for a known and an unknown
// issue count, interactive and not.
func TestConfirmReinitDestroy(t *testing.T) {
	const target = "/work/project/.beads"
	countErr := errors.New("open store: connection refused")
	tests := []struct {
		name        string
		count       int
		countErr    error
		interactive bool
		answer      string
		token       string
		wantCode    int // 0 means proceed (nil error)
		wantOut     []string
	}{
		{
			name:     "unknown count, non-interactive, no token",
			countErr: countErr,
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Existing issues: unknown (could not count them: open store: connection refused)", "bd doctor", "Refusing to destroy an unknown number of issues", "bd help init-safety"},
		},
		{
			name:     "unknown count, non-interactive, wrong token",
			countErr: countErr,
			token:    "DESTROY-other",
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Refusing to destroy an unknown number of issues"},
		},
		{
			name:     "unknown count, non-interactive, token",
			countErr: countErr,
			token:    "DESTROY-bd",
			wantOut:  []string{"Existing issues: unknown", "Destroy token accepted"},
		},
		{
			name:     "known count, non-interactive, no token",
			count:    3,
			wantCode: ExitDestroyTokenMissing,
			wantOut:  []string{"Existing issues: 3", "Refusing to destroy 3 issues in non-interactive mode."},
		},
		{
			name:    "known count, non-interactive, token",
			count:   3,
			token:   "DESTROY-bd",
			wantOut: []string{"Existing issues: 3", "Destroy token accepted"},
		},
		{
			name:        "known count, interactive, confirmed",
			count:       3,
			interactive: true,
			answer:      "destroy 3 issues\n",
			wantOut:     []string{"Type 'destroy 3 issues' to confirm"},
		},
		{
			name:        "known count, interactive, declined",
			count:       3,
			interactive: true,
			answer:      "no\n",
			wantCode:    ExitLocalExistsRefused,
			wantOut:     []string{"Aborted. Database was NOT modified."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := confirmReinitDestroy(&buf, strings.NewReader(tt.answer), tt.interactive, target, tt.count, tt.countErr, "bd", tt.token)
			out := buf.String()
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("want nil (proceed), got %v\n%s", err, out)
				}
			} else {
				requireExitCode(t, err, tt.wantCode)
			}
			if !strings.Contains(out, "Target: "+target) {
				t.Errorf("warning must name the target, got:\n%s", out)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q, got:\n%s", want, out)
				}
			}
			if strings.Contains(out, "DESTROY-") {
				t.Errorf("output must never print the destroy token value, got:\n%s", out)
			}
		})
	}

	t.Run("count error is spelled, not printed raw", func(t *testing.T) {
		var buf bytes.Buffer
		err := confirmReinitDestroy(&buf, strings.NewReader(""), false, target, 0, errors.New("bad \x1b[2Jterminal"), "bd", "")
		requireExitCode(t, err, ExitDestroyTokenMissing)
		if strings.Contains(buf.String(), "\x1b") {
			t.Errorf("count error reached the terminal raw:\n%q", buf.String())
		}
	})
}

// TestCountReinitTargetIssues pins what the reinit preflight treats as an
// uncountable workspace: one it cannot open is an error, never zero.
func TestCountReinitTargetIssues(t *testing.T) {
	t.Run("unparseable metadata.json is an error", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		writeReinitTargetMetadata(t, beadsDir, []byte("{not json"))
		if _, err := countReinitTargetIssues(beadsDir); err == nil {
			t.Fatal("want an error for an unloadable metadata.json, got nil")
		}
	})

	t.Run("server mode with a local copy and no server is an error", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
		t.Setenv("BEADS_DOLT_AUTO_START", "0")
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		data, _ := json.Marshal(map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "server",
			"dolt_database": "myproject",
			"project_id":    "existing-0000-1111-2222-333344445555",
		})
		writeReinitTargetMetadata(t, beadsDir, data)
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "myproject", ".dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := countReinitTargetIssues(beadsDir); err == nil {
			t.Fatal("want an error: a local copy of the database is never proof that it is absent")
		}
	})
}
