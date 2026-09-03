// Tests for bd-5247: `bd update --set-metadata a=1 b=2` silently turns `b=2`
// into a positional issue id (--set-metadata takes one key=value per flag).
// Before this guard, `a=1` was written and only the unbound pairs failed, so a
// caller could not tell a full write from a 1-of-N write. The guard refuses a
// `=`-bearing positional before ANY write, so no partial update lands.
//
// This file MUST NOT carry a cgo build tag: it exercises the default sqlite
// backend via a bd binary built with the gms_pure_go tag, reusing the helpers
// in update_multi_id_exit_test.go (same package).

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestErrStrayFlagValuePositional(t *testing.T) {
	// A positional carrying '=' is a mis-typed flag value and must be refused.
	if err := errStrayFlagValuePositional([]string{"test-abc", "probe_b=2"}); err == nil {
		t.Fatal("errStrayFlagValuePositional with a '='-bearing positional = nil, want error")
	}
	// Plain issue ids (no '=') must pass through untouched.
	if err := errStrayFlagValuePositional([]string{"test-abc", "test-def"}); err != nil {
		t.Fatalf("errStrayFlagValuePositional with plain ids = %v, want nil", err)
	}
	if err := errStrayFlagValuePositional(nil); err != nil {
		t.Fatalf("errStrayFlagValuePositional(nil) = %v, want nil", err)
	}
}

// showStrayMetadata fetches an issue's metadata map via bd show --json.
func showStrayMetadata(t *testing.T, bd, dir, id string) map[string]interface{} {
	t.Helper()
	stdout, stderr, code := runBDMultiID(t, bd, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("bd show %s failed (exit %d):\nstdout:\n%s\nstderr:\n%s", id, code, stdout, stderr)
	}
	var details []struct {
		ID       string                 `json:"id"`
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(stdout), &details); err != nil {
		t.Fatalf("parsing show --json for %s: %v\n%s", id, err, stdout)
	}
	if len(details) != 1 || details[0].ID != id {
		t.Fatalf("show --json for %s returned unexpected issues:\n%s", id, stdout)
	}
	return details[0].Metadata
}

func TestUpdateStrayMetadataPositionalRefusedBeforeWrite(t *testing.T) {
	bd, dir := setupMultiIDUpdateDB(t)
	id := createMultiIDUpdateIssue(t, bd, dir, "stray metadata target")

	// Only `probe_a=1` binds to the flag; `probe_b=2` and `probe_c=3` land as
	// positional ids. The command must refuse before writing anything.
	stdout, stderr, code := runBDMultiID(t, bd, dir,
		"update", id, "--set-metadata", "probe_a=1", "probe_b=2", "probe_c=3")
	if code == 0 {
		t.Errorf("bd update with a '='-bearing positional exited 0, want nonzero\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "probe_b=2") {
		t.Errorf("stderr does not name the mis-typed pair probe_b=2:\n%s", stderr)
	}

	// The refusal is before any write: no pair — not even the bound probe_a —
	// may have landed. This is the half a message-only fix would leave broken.
	meta := showStrayMetadata(t, bd, dir, id)
	if len(meta) != 0 {
		t.Errorf("metadata is %v, want empty: refusal must prevent the partial write", meta)
	}
}

func TestUpdateSetMetadataRepeatedFlagStillWrites(t *testing.T) {
	bd, dir := setupMultiIDUpdateDB(t)
	id := createMultiIDUpdateIssue(t, bd, dir, "correct form target")

	// The correct form (one flag per pair) has no stray positional and must
	// still apply normally — the guard does not break valid usage.
	stdout, stderr, code := runBDMultiID(t, bd, dir,
		"update", id, "--set-metadata", "probe_a=1", "--set-metadata", "probe_b=2")
	if code != 0 {
		t.Fatalf("bd update with repeated --set-metadata exited %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	meta := showStrayMetadata(t, bd, dir, id)
	if meta["probe_a"] == nil || meta["probe_b"] == nil {
		t.Errorf("metadata = %v, want both probe_a and probe_b written", meta)
	}
}
