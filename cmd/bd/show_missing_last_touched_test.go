//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShowMissingIDPreservesLastTouched covers a failed show lookup at the
// process boundary. A missing ID in text or JSON mode must leave an existing
// last-touched marker byte-for-byte unchanged, including mtime, rather than
// replacing it with the unknown raw argument.
func TestShowMissingIDPreservesLastTouched(t *testing.T) {
	binPath := buildBDUnderTest(t)
	workDir := t.TempDir()
	initBeadsWorkspace(t, binPath, workDir)

	id := runBDStdout(t, binPath, workDir, "q", "missing-id last-touched fixture")
	if id == "" {
		t.Fatal("bd q returned empty issue ID")
	}
	const missingID = "bd-does-not-exist"
	lastTouchedPath := filepath.Join(workDir, ".beads", lastTouchedFile)

	for _, mode := range []struct {
		name string
		args []string
	}{
		{name: "text", args: []string{"show", missingID}},
		{name: "json", args: []string{"show", missingID, "--json"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			sentinel := []byte(id + "\n")
			mtime := setLastTouchedSentinel(t, lastTouchedPath, sentinel)

			result := runLastTouchedProcess(t, binPath, workDir, mode.args...)
			if result.exitCode == 0 {
				t.Fatalf("show of missing ID exit code = 0, want non-zero\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
			}
			assertLastTouchedUnchanged(t, lastTouchedPath, sentinel, mtime)
		})
	}
}

// TestShowResolvedIDLastTouched covers a show that resolves at least one ID
// while other arguments miss or are written as partial IDs. The marker must
// record the first argument that actually resolved, as its canonical issue ID:
// never a missing argument, and never a partial ID as typed. A bare-hash
// argument resolving to its full ID is a deliberate change from the previous
// raw-argument write. This test is new; the missing-only cases above are the
// original regression.
func TestShowResolvedIDLastTouched(t *testing.T) {
	binPath := buildBDUnderTest(t)
	workDir := t.TempDir()
	initBeadsWorkspace(t, binPath, workDir)

	firstID := runBDStdout(t, binPath, workDir, "q", "resolved-id last-touched fixture one")
	secondID := runBDStdout(t, binPath, workDir, "q", "resolved-id last-touched fixture two")
	if firstID == "" || secondID == "" {
		t.Fatalf("bd q returned an empty issue ID: first=%q second=%q", firstID, secondID)
	}
	if firstID == secondID {
		t.Fatalf("bd q returned the same issue ID twice: %q", firstID)
	}
	const missingID = "bd-does-not-exist"
	// The workspace prefix is "test", so the bare hash is a partial ID that
	// resolves to the full ID.
	partialID := strings.TrimPrefix(firstID, "test-")
	if partialID == firstID {
		t.Fatalf("issue ID %q has no test- prefix, cannot derive a partial ID", firstID)
	}
	lastTouchedPath := filepath.Join(workDir, ".beads", lastTouchedFile)
	staleMarker := []byte("stale-marker\n")

	modes := []struct {
		name  string
		flags []string
	}{
		{name: "text"},
		{name: "json", flags: []string{"--json"}},
		{name: "short", flags: []string{"--short"}},
	}
	cases := []struct {
		name string
		ids  []string
		want string
	}{
		{name: "missing then found", ids: []string{missingID, firstID}, want: firstID},
		{name: "missing then two found", ids: []string{missingID, firstID, secondID}, want: firstID},
		{name: "found then missing", ids: []string{firstID, missingID}, want: firstID},
		{name: "partial ID", ids: []string{partialID}, want: firstID},
		{name: "missing then partial ID", ids: []string{missingID, partialID}, want: firstID},
	}

	for _, mode := range modes {
		for _, tc := range cases {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				mtime := setLastTouchedSentinel(t, lastTouchedPath, staleMarker)

				args := append([]string{"show"}, tc.ids...)
				args = append(args, mode.flags...)
				result := runLastTouchedProcess(t, binPath, workDir, args...)
				if result.exitCode != 0 {
					t.Fatalf("bd %v exit code = %d, want 0\nstdout: %s\nstderr: %s", args, result.exitCode, result.stdout, result.stderr)
				}

				got, err := os.ReadFile(lastTouchedPath)
				if err != nil {
					t.Fatalf("read last-touched marker: %v", err)
				}
				if string(got) != tc.want+"\n" {
					t.Fatalf("bd %v last-touched marker = %q, want %q", args, got, tc.want+"\n")
				}
				info, err := os.Stat(lastTouchedPath)
				if err != nil {
					t.Fatalf("stat last-touched marker: %v", err)
				}
				if !info.ModTime().After(mtime) {
					t.Fatalf("bd %v did not advance last-touched mtime: got %v, previous %v", args, info.ModTime(), mtime)
				}
			})
		}
	}

	// Strict readonly must keep leaving the marker alone when a mixed lookup
	// resolves an issue.
	t.Run("readonly missing then found", func(t *testing.T) {
		mtime := setLastTouchedSentinel(t, lastTouchedPath, staleMarker)
		result := runLastTouchedProcess(t, binPath, workDir, "--readonly", "show", missingID, firstID, "--json")
		if result.exitCode != 0 {
			t.Fatalf("readonly show exit code = %d, want 0\nstdout: %s\nstderr: %s", result.exitCode, result.stdout, result.stderr)
		}
		assertLastTouchedUnchanged(t, lastTouchedPath, staleMarker, mtime)
	})
}
