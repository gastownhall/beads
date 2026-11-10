package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestShowNotFoundHint_PointsAtHistoryAndAcknowledgesDeletion guards
// ga-m6inyb: bd show's "not found" message must stop implying an ID never
// existed (it may have existed and been deleted/purged, which leaves no
// trace in the live tables) and must tell the reader how to check further.
func TestShowNotFoundHint_PointsAtHistoryAndAcknowledgesDeletion(t *testing.T) {
	hint := showNotFoundHint("bd-1234")

	if !strings.Contains(hint, "bd history bd-1234") {
		t.Errorf("expected hint to name the exact id in a 'bd history' pointer, got: %q", hint)
	}
	if !strings.Contains(hint, "deleted") && !strings.Contains(hint, "purged") {
		t.Errorf("expected hint to acknowledge deletion/purge as a possibility, got: %q", hint)
	}
	if !strings.Contains(hint, "never existed") {
		t.Errorf("expected hint to name 'never existed' as only ONE of the possibilities, got: %q", hint)
	}
}

func TestShowNotFoundHint_IsIDSpecific(t *testing.T) {
	a := showNotFoundHint("bd-aaa")
	b := showNotFoundHint("bd-bbb")

	if a == b {
		t.Errorf("expected hint text to vary with id, got identical output: %q", a)
	}
	if !strings.Contains(a, "bd-aaa") || strings.Contains(a, "bd-bbb") {
		t.Errorf("expected hint for bd-aaa to reference only its own id, got: %q", a)
	}
}

// foreignPrefixTown makes a .beads directory holding routes (no routes.jsonl
// when routes is empty) and returns it.
func foreignPrefixTown(t *testing.T, routes string) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}
	if routes != "" {
		if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes+"\n"), 0o600); err != nil {
			t.Fatalf("write routes.jsonl: %v", err)
		}
	}
	return beadsDir
}

// prefixConfigStub is the prefix config showNotFoundHintIn reads.
type prefixConfigStub map[string]string

func (s prefixConfigStub) GetConfig(_ context.Context, key string) (string, error) {
	return s[key], nil
}

// TestShowNotFoundHintIn_ForeignPrefixIsNotCalledAbsent pins GH#5259: an id
// whose prefix this workspace's store does not issue, and that no routes.jsonl
// route names, may never have been looked up where it lives, so the hint
// must say that rather than suggest the id never existed. Every other miss
// keeps showNotFoundHint.
func TestShowNotFoundHintIn_ForeignPrefixIsNotCalledAbsent(t *testing.T) {
	ctx := context.Background()
	rig := prefixConfigStub{"issue_prefix": "openclaw"}

	town := foreignPrefixTown(t, "")
	foreign := showNotFoundHintIn(ctx, rig, town, "herald-x7k", false)
	for _, want := range []string{`prefix "herald"`, "openclaw", "does not mean the issue is absent", `issues "herald" IDs`, filepath.Join(town, "routes.jsonl")} {
		if !strings.Contains(foreign, want) {
			t.Errorf("foreign-prefix hint missing %q: %q", want, foreign)
		}
	}
	for _, notWant := range []string{"never existed", "bd history", "deleted"} {
		if strings.Contains(foreign, notWant) {
			t.Errorf("foreign-prefix hint still points at %q: %q", notWant, foreign)
		}
	}

	for _, tc := range []struct {
		name     string
		store    prefixConfigStub
		routes   string
		id       string
		beadsDir bool
	}{
		{name: "own prefix", store: rig, id: "openclaw-zz9", beadsDir: true},
		{name: "own prefix with trailing hyphen in config", store: prefixConfigStub{"issue_prefix": "openclaw-"}, id: "openclaw-zz9", beadsDir: true},
		{name: "allowed prefix", store: prefixConfigStub{"issue_prefix": "openclaw", "allowed_prefixes": "op, herald-"}, id: "herald-x7k", beadsDir: true},
		{name: "routed prefix", store: rig, routes: `{"prefix":"herald-","path":"herald"}`, id: "herald-x7k", beadsDir: true},
		{name: "no issue_prefix configured", store: prefixConfigStub{}, id: "herald-x7k", beadsDir: true},
		{name: "bare hash", store: rig, id: "gea4h", beadsDir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := ""
			if tc.beadsDir {
				dir = foreignPrefixTown(t, tc.routes)
			}
			if got, want := showNotFoundHintIn(ctx, tc.store, dir, tc.id, false), showNotFoundHint(tc.id); got != want {
				t.Errorf("hint = %q, want showNotFoundHint's %q", got, want)
			}
		})
	}

	if got, want := showNotFoundHintIn(ctx, nil, "", "herald-x7k", false), showNotFoundHint("herald-x7k"); got != want {
		t.Errorf("no store: hint = %q, want %q", got, want)
	}
}

// showMissStubStore holds no issues and issues the prefix "openclaw": every
// lookup bd show's direct route makes misses. The embedded nil DoltStorage
// makes any other call panic.
type showMissStubStore struct {
	storage.DoltStorage
}

func (showMissStubStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return nil, nil
}

func (showMissStubStore) SearchIssueIDs(context.Context, string, types.IssueFilter) ([]string, error) {
	return nil, nil
}

func (showMissStubStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "openclaw", nil
	}
	return "", nil
}

func (showMissStubStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{"issue_prefix": "openclaw"}, nil
}

func (showMissStubStore) GetIssue(context.Context, string) (*types.Issue, error) {
	return nil, storage.ErrNotFound
}

// TestShowDirectRoute_ForeignPrefixMissHint runs bd show's direct route on a
// store that issues "openclaw" in a workspace with no routes.jsonl, the shape
// GH#5259 reports, and pins the hint each miss prints.
func TestShowDirectRoute_ForeignPrefixMissHint(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })
	store, rootCtx, jsonOutput, proxiedServerMode = showMissStubStore{}, context.Background(), false, false
	dbPath = filepath.Join(foreignPrefixTown(t, ""), "dolt")

	for _, tc := range []struct {
		id, want, notWant string
	}{
		{id: "herald-x7k", want: "does not mean the issue is absent", notWant: "never existed"},
		{id: "openclaw-zz9", want: "never existed", notWant: "does not mean the issue is absent"},
	} {
		var err error
		stderr := captureStderr(t, func() { err = showCmd.RunE(showCmd, []string{tc.id}) })
		if err == nil {
			t.Errorf("bd show %s: want the not-found exit, got nil", tc.id)
		}
		if !strings.Contains(stderr, "Issue "+tc.id+" not found") || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, tc.notWant) {
			t.Errorf("bd show %s stderr = %q, want %q and not %q", tc.id, stderr, tc.want, tc.notWant)
		}
	}
}
