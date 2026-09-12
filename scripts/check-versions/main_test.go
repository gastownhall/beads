package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/versioncheck"
)

func TestRunExpectedVersionContract(t *testing.T) {
	root := writeReleaseFixture(t, "1.1.0")

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "aligned version",
			args:       []string{"--expect", "1.1.0"},
			wantCode:   0,
			wantStdout: "✓ Version files and released-docs policy pass for: 1.1.0",
		},
		{
			name:       "mismatched version",
			args:       []string{"--expect", "9.9.9"},
			wantCode:   1,
			wantStdout: "Canonical version (from version.go): 1.1.0",
			wantStderr: "Canonical release version 1.1.0 does not match required version 9.9.9",
		},
		{
			name:       "empty version",
			args:       []string{"--expect="},
			wantCode:   2,
			wantStderr: "--expect requires a non-empty VERSION",
		},
		{
			name:       "duplicate version",
			args:       []string{"--expect", "1.1.0", "--expect", "1.1.0"},
			wantCode:   2,
			wantStderr: "--expect may be specified only once",
		},
		{
			name:       "unknown argument",
			args:       []string{"--unknown"},
			wantCode:   2,
			wantStderr: "flag provided but not defined: -unknown",
		},
		{
			name:       "positional argument",
			args:       []string{"1.1.0"},
			wantCode:   2,
			wantStderr: "usage: scripts/check-versions.sh [--expect VERSION]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(
				test.args,
				root,
				&stdout,
				&stderr,
				func(string) (bool, error) { return false, nil },
			)

			if code != test.wantCode {
				t.Fatalf(
					"exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s",
					code,
					test.wantCode,
					stdout.String(),
					stderr.String(),
				)
			}
			if test.wantStdout != "" && !strings.Contains(stdout.String(), test.wantStdout) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), test.wantStdout)
			}
			if test.wantStdout == "" && stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), test.wantStderr)
			}
			if test.wantStderr == "" && stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunPreservesOptionalUVLockFreshnessCheck(t *testing.T) {
	root := writeReleaseFixture(t, "1.1.0")
	for _, test := range []struct {
		name       string
		check      versioncheck.UVLockChecker
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name: "fresh",
			check: func(string) (bool, error) {
				return true, nil
			},
			wantCode:   0,
			wantStdout: "✓ MCP uv.lock: fresh (uv lock --check)",
		},
		{
			name: "stale",
			check: func(string) (bool, error) {
				return true, errors.New("stale")
			},
			wantCode:   1,
			wantStderr: "❌ MCP uv.lock: stale",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(nil, root, &stdout, &stderr, test.check)
			if code != test.wantCode {
				t.Fatalf("exit code = %d, want %d", code, test.wantCode)
			}
			if !strings.Contains(stdout.String(), test.wantStdout) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), test.wantStdout)
			}
			if !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), test.wantStderr)
			}
			if test.wantCode != 0 && strings.Contains(
				stdout.String(),
				"Version files and released-docs policy pass",
			) {
				t.Fatal("stale uv.lock emitted a success result")
			}
		})
	}
}

func TestRunRejectsTrackedHookMarkerMismatch(t *testing.T) {
	root := writeReleaseFixture(t, "1.1.0")
	if err := os.WriteFile(
		filepath.Join(root, ".githooks", "pre-push"),
		[]byte(
			"# --- BEGIN BEADS INTEGRATION v1.1.0 ---\n"+
				"body\n# --- END BEADS INTEGRATION v9.9.9 ---\n",
		),
		0o755,
	); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(nil, root, &stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(
		stdout.String(),
		".githooks/pre-push END marker: 9.9.9 (expected 1.1.0)",
	) {
		t.Fatalf("stdout = %q, want tracked-hook mismatch", stdout.String())
	}
	if !strings.Contains(
		stderr.String(),
		".githooks/pre-push END marker: 9.9.9 (expected 1.1.0)",
	) {
		t.Fatalf("stderr = %q, want tracked-hook mismatch summary", stderr.String())
	}
	if strings.Contains(stdout.String(), "Version files and released-docs policy pass") {
		t.Fatal("tracked-hook mismatch emitted a success result")
	}
}

func writeReleaseFixture(t *testing.T, version string) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"go.mod":                     "module github.com/steveyegge/beads\n\ngo 1.26\n",
		"cmd/bd/version.go":          "package main\n\nvar Version = \"" + version + "\"\n",
		"scripts/update-versions.sh": "#!/bin/sh\n",
		"integrations/beads-mcp/pyproject.toml": "[project]\nversion = \"" +
			version + "\"\n",
		"integrations/beads-mcp/src/beads_mcp/__init__.py": "__version__ = \"" +
			version + "\"\n",
		"plugins/beads/.claude-plugin/plugin.json": `{"version":"` + version + `"}`,
		"plugins/beads/.codex-plugin/plugin.json":  `{"version":"` + version + `"}`,
		".claude-plugin/marketplace.json": `{"plugins":[{"version":"` +
			version + `"}]}`,
		"npm-package/package.json": `{"version":"` + version + `"}`,
		"integrations/beads-mcp/uv.lock": "version = 1\nrevision = 3\n\n" +
			"[[package]]\nname = \"beads-mcp\"\nversion = \"" + version + "\"\n",
		".githooks/pre-push": "# --- BEGIN BEADS INTEGRATION v" + version + " ---\n" +
			"body\n# --- END BEADS INTEGRATION v" + version + " ---\n",
	}
	for relativePath, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", relativePath, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relativePath, err)
		}
	}
	return root
}
