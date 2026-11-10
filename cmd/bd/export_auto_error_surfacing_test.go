package main

// GH#6163 tests for the auto-export failure policy: maybeAutoExport must
// return operational failures instead of warning and exiting 0 while the
// JSONL silently drifts from the store. Deliberate safety refusals and
// unsupported state-hash capabilities stay warning-only skips.
//
// This file is kept separate from export_auto_test.go so the checkpoint
// pipeline can re-apply its triage repro (which appends to that file)
// without patch conflicts.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// stateHashReadErrStore fails the StateHasher read maybeAutoExport uses for
// change detection, while keeping the GetCurrentCommit surface intact so the
// failure is attributable to the state hash alone.
type stateHashReadErrStore struct {
	fakeHeadOnlyStore
}

func (f *stateHashReadErrStore) GetStateHash(_ context.Context) (string, error) {
	return "", errStateHashRead
}

// headCommitReadErrStore does not implement storage.StateHasher and fails
// GetCurrentCommit, so storeStateHash fails through its fallback arm.
type headCommitReadErrStore struct {
	fakeHeadOnlyStore
}

func (f *headCommitReadErrStore) GetCurrentCommit(_ context.Context) (string, error) {
	f.currentCommitCalls++
	return "", errHeadCommitRead
}

var (
	errStateHashRead  = errors.New("state hash unavailable")
	errHeadCommitRead = errors.New("current commit unavailable")
)

// TestMaybeAutoExportFailsWhenStateHashUnreadable pins AC-1 of GH#6163: an
// operational state-hash failure returns a contextual error wrapping the
// cause and naming the export destination, leaves the existing JSONL and
// export state byte-for-byte unchanged, and prints no warning itself (the
// caller's HandleError is the single print). The seeded recent Timestamp
// pins the ordering: the state hash is read before the throttle, so the
// failure must surface even while the export interval window is closed.
func TestMaybeAutoExportFailsWhenStateHashUnreadable(t *testing.T) {
	cases := []struct {
		name      string
		store     storage.DoltStorage
		wantCause error
	}{
		{"state hasher", &stateHashReadErrStore{}, errStateHashRead},
		{"GetCurrentCommit fallback", &headCommitReadErrStore{}, errHeadCommitRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			config.Set("export.auto", true)
			saveAndRestoreGlobals(t)
			t.Setenv("BD_GIT_HOOK", "")
			store = tc.store

			dir := autoExportTestDir(t)
			beadsDir := filepath.Join(dir, ".beads")
			seedJSONL := filepath.Join(beadsDir, "issues.jsonl")
			seed := `{"_type":"issue","id":"bd-seed","issue_type":"task","title":"seed"}` + "\n"
			if err := os.WriteFile(seedJSONL, []byte(seed), 0o600); err != nil {
				t.Fatal(err)
			}
			saveExportAutoState(beadsDir, &exportAutoState{
				LastDoltCommit: "hash-before-write",
				LastDiffAnchor: "anchor-before-write",
				Timestamp:      time.Now(),
				Issues:         1,
			})
			seedState := filepath.Join(beadsDir, exportAutoStateFile)
			seedStateBytes, err := os.ReadFile(seedState)
			if err != nil {
				t.Fatal(err)
			}

			var got error
			stderr := captureStderr(t, func() { got = maybeAutoExport(context.Background(), false) })
			if !errors.Is(got, tc.wantCause) {
				t.Fatalf("maybeAutoExport err = %v, want it to wrap %v", got, tc.wantCause)
			}
			if !strings.Contains(got.Error(), seedJSONL) {
				t.Fatalf("error %q must name the resolved export destination %q", got.Error(), seedJSONL)
			}
			if strings.Contains(stderr, "Warning") {
				t.Fatalf("maybeAutoExport printed %q; the error path must leave the single print to the caller", stderr)
			}
			assertFileUnchanged(t, seedJSONL, seed)
			assertFileUnchanged(t, seedState, string(seedStateBytes))
		})
	}
}
