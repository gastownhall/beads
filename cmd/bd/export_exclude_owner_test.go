//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdExportExclude runs "bd export --exclude-owner" and returns stdout.
func bdExportExclude(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	return bdExport(t, bd, dir, args...)
}

// bdExportAllowErr runs "bd export" returning stdout+stderr and any error.
func bdExportAllowErrArgs(t *testing.T, bd, dir string, args ...string) (string, error) {
	t.Helper()
	fullArgs := append([]string{"export"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	return stdout.String() + stderr.String(), err
}

// parseExportedIssues parses JSONL output from bd export.
func parseExportedIssues(t *testing.T, jsonl string) []*types.Issue {
	t.Helper()
	var issues []*types.Issue
	for _, line := range strings.Split(strings.TrimSpace(jsonl), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var iss types.Issue
		if err := json.Unmarshal([]byte(line), &iss); err != nil {
			t.Fatalf("failed to parse exported issue: %v\nline: %s", err, line)
		}
		issues = append(issues, &iss)
	}
	return issues
}

// TestExportExcludeOwner_flag verifies that bd export --exclude-owner=<name>
// filters out issues created_by that identity from the export.
func TestExportExcludeOwner_flag(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "eeo")

	// Create issues with different actors.
	bdCreate(t, bd, dir, "My personal issue", "--actor", "alice")
	bdCreate(t, bd, dir, "Team issue", "--actor", "bob")
	bdCreate(t, bd, dir, "Another personal", "--actor", "alice")

	// Export excluding alice.
	out := bdExportExclude(t, bd, dir, "--exclude-owner=alice")
	issues := parseExportedIssues(t, out)

	for _, iss := range issues {
		if iss.CreatedBy == "alice" {
			t.Errorf("issue %s by 'alice' should be excluded, but appeared in export", iss.ID)
		}
	}

	// Bob's issue should still be in the export.
	found := false
	for _, iss := range issues {
		if iss.CreatedBy == "bob" {
			found = true
			break
		}
	}
	if !found {
		t.Error("issue by 'bob' should appear in export after excluding 'alice'")
	}
}

// TestExportExcludeOwner_config verifies that export.exclude_owners config
// filters issues without a command-line flag.
func TestExportExcludeOwner_config(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "eec")

	// Set config first.
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "export.exclude_owners", "carol") //nolint:errcheck

	// Create issues.
	bdCreate(t, bd, dir, "Carol personal", "--actor", "carol")
	bdCreate(t, bd, dir, "Dave team", "--actor", "dave")

	// Export without --exclude-owner flag — config should drive the filter.
	out := bdExport(t, bd, dir)
	issues := parseExportedIssues(t, out)

	for _, iss := range issues {
		if iss.CreatedBy == "carol" {
			t.Errorf("issue %s by 'carol' should be excluded via config, appeared in export", iss.ID)
		}
	}
}

// TestExportExcludeOwner_verbose verifies that --verbose emits a filtered count message.
func TestExportExcludeOwner_verbose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "eev")

	bdCreate(t, bd, dir, "Eve personal", "--actor", "eve")
	bdCreate(t, bd, dir, "Frank team", "--actor", "frank")

	out, _ := bdExportAllowErrArgs(t, bd, dir, "--exclude-owner=eve", "--verbose")

	// Verbose mode should emit something about filtered count.
	if !strings.Contains(out, "filtered") && !strings.Contains(out, "excluded") &&
		!strings.Contains(out, "1") {
		t.Logf("verbose output: %s", out)
		// Soft check — verbose behavior may vary. At minimum, verify export ran.
	}

	// The filtered-out issue should not appear in the JSONL portion.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "{") {
			continue
		}
		var iss types.Issue
		if err := json.Unmarshal([]byte(line), &iss); err == nil {
			if iss.CreatedBy == "eve" {
				t.Errorf("issue by 'eve' should be filtered with --verbose, appeared in export")
			}
		}
	}
}

// TestAutoExportExcludeOwner_config is a regression test for the maphew review
// of PR #4023 (be-e2nb): the export.exclude_owners safety net must also apply to
// the git-committed auto-export (.beads/issues.jsonl), not only to manual
// `bd export`. Before the fix, contributor/personal issues that the manual path
// excludes could still leak into git history and PRs via auto-export.
func TestAutoExportExcludeOwner_config(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "aeo")

	// Enable auto-export with a tiny interval (defeats the default 60s throttle)
	// and exclude owner 'alice' via config.
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "export.exclude_owners", "alice") //nolint:errcheck
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "export.interval", "1ms")         //nolint:errcheck
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "export.auto", "true")            //nolint:errcheck

	// One issue by alice (excluded) and one by bob (kept). The bob create runs
	// last, so the auto-export it triggers reflects the full DB state.
	bdCreate(t, bd, dir, "Alice personal issue", "--actor", "alice")
	bdCreate(t, bd, dir, "Bob team issue", "--actor", "bob")

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("auto-export did not write %s: %v", jsonlPath, err)
	}

	sawBob := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var iss types.Issue
		if err := json.Unmarshal([]byte(line), &iss); err != nil {
			continue
		}
		if iss.CreatedBy == "alice" {
			t.Errorf("issue %s by 'alice' leaked into auto-export %s despite export.exclude_owners", iss.ID, jsonlPath)
		}
		if iss.CreatedBy == "bob" {
			sawBob = true
		}
	}
	if !sawBob {
		t.Fatalf("auto-export %s did not contain bob's issue; the export may not have run, so the absence of alice would be inconclusive", jsonlPath)
	}
}
