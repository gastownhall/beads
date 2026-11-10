package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// prefixConfigFailStub is prefixConfigStub whose read of failKey fails.
type prefixConfigFailStub struct {
	prefixConfigStub
	failKey string
}

func (s prefixConfigFailStub) GetConfig(ctx context.Context, key string) (string, error) {
	if key == s.failKey {
		return "", errors.New("config read failed")
	}
	return s.prefixConfigStub.GetConfig(ctx, key)
}

// TestShowNotFoundHintIn_KeepsExistingHintWhenUnsure pins AC-3 (GH#5259):
// the foreign-prefix hint is printed only when the configuration reliably
// says the id's prefix is foreign and nothing searched elsewhere for it. An
// own or allowed prefix, a config read that fails, an unreadable routes.jsonl,
// any matching route (a self-route included) or a routed search keeps
// showNotFoundHint; a missing, empty, unmatched or hyphenless routes.jsonl
// does not stop the new hint.
func TestShowNotFoundHintIn_KeepsExistingHintWhenUnsure(t *testing.T) {
	ctx := context.Background()
	rig := prefixConfigStub{"issue_prefix": "openclaw"}

	unreadableRoutes := foreignPrefixTown(t, "")
	if err := os.Mkdir(filepath.Join(unreadableRoutes, "routes.jsonl"), 0o755); err != nil {
		t.Fatalf("make routes.jsonl a directory: %v", err)
	}

	keep := []struct {
		name     string
		store    prefixConfigReader
		beadsDir string
		id       string
		searched bool
	}{
		{name: "own prefix with surrounding whitespace in config", store: prefixConfigStub{"issue_prefix": "  openclaw- "}, beadsDir: foreignPrefixTown(t, ""), id: "openclaw-zz9"},
		{name: "allowed prefix with whitespace and trailing hyphen", store: prefixConfigStub{"issue_prefix": "openclaw", "allowed_prefixes": " op ,  herald- "}, beadsDir: foreignPrefixTown(t, ""), id: "herald-x7k"},
		{name: "allowed multi-hyphen prefix with an all-letter suffix", store: prefixConfigStub{"issue_prefix": "openclaw", "allowed_prefixes": "claude-os"}, beadsDir: foreignPrefixTown(t, ""), id: "claude-os-abcd"},
		{name: "issue_prefix read fails", store: prefixConfigFailStub{rig, "issue_prefix"}, beadsDir: foreignPrefixTown(t, ""), id: "herald-x7k"},
		{name: "allowed_prefixes read fails", store: prefixConfigFailStub{rig, "allowed_prefixes"}, beadsDir: foreignPrefixTown(t, ""), id: "herald-x7k"},
		{name: "whitespace-only issue_prefix", store: prefixConfigStub{"issue_prefix": "  "}, beadsDir: foreignPrefixTown(t, ""), id: "herald-x7k"},
		{name: "unreadable routes.jsonl", store: rig, beadsDir: unreadableRoutes, id: "herald-x7k"},
		{name: "self-route", store: rig, beadsDir: foreignPrefixTown(t, `{"prefix":"herald-","path":"."}`), id: "herald-x7k"},
		{name: "overlapping valid routes", store: rig, beadsDir: foreignPrefixTown(t, `{"prefix":"hq-","path":"town"}`+"\n"+`{"prefix":"hq-cv-","path":"convoys"}`), id: "hq-cv-x7k"},
		{name: "a routed store was searched", store: rig, beadsDir: foreignPrefixTown(t, ""), id: "herald-x7k", searched: true},
		{name: "a routed store was searched, no workspace", store: rig, id: "herald-x7k", searched: true},
	}
	for _, tc := range keep {
		t.Run("keeps/"+tc.name, func(t *testing.T) {
			if got, want := showNotFoundHintIn(ctx, tc.store, tc.beadsDir, tc.id, tc.searched), showNotFoundHint(tc.id); got != want {
				t.Errorf("hint = %q, want showNotFoundHint's %q", got, want)
			}
		})
	}

	foreign := []struct {
		name     string
		beadsDir string
		wantPath bool
	}{
		{name: "no routes.jsonl", beadsDir: foreignPrefixTown(t, ""), wantPath: true},
		{name: "empty routes.jsonl", beadsDir: foreignPrefixTown(t, "# no routes yet"), wantPath: true},
		{name: "unmatched route", beadsDir: foreignPrefixTown(t, `{"prefix":"gt-","path":"gastown"}`), wantPath: true},
		{name: "hyphenless route entry", beadsDir: foreignPrefixTown(t, `{"prefix":"herald","path":"herald"}`), wantPath: true},
		{name: "no resolved workspace"},
	}
	for _, tc := range foreign {
		t.Run("new hint/"+tc.name, func(t *testing.T) {
			got := showNotFoundHintIn(ctx, rig, tc.beadsDir, "herald-x7k", false)
			if !strings.Contains(got, `prefix "herald"`) || !strings.Contains(got, "does not mean the issue is absent") {
				t.Fatalf("hint = %q, want the foreign-prefix hint", got)
			}
			routesPath := filepath.Join(tc.beadsDir, "routes.jsonl")
			if tc.wantPath && !strings.Contains(got, routesPath) {
				t.Errorf("hint = %q, want it to name %s", got, routesPath)
			}
			if !tc.wantPath && (!strings.Contains(got, "routes.jsonl route is available") || strings.Contains(got, string(filepath.Separator)+"routes.jsonl")) {
				t.Errorf("hint = %q, want it to say no workspace route is available without naming a path", got)
			}
		})
	}

	// An allowed multi-hyphen prefix covers its own IDs only: a sibling
	// prefix that shares its first segment is still foreign.
	t.Run("new hint/allowed multi-hyphen prefix does not swallow siblings", func(t *testing.T) {
		allowed := prefixConfigStub{"issue_prefix": "openclaw", "allowed_prefixes": "claude-os"}
		got := showNotFoundHintIn(ctx, allowed, foreignPrefixTown(t, ""), "claude-x1", false)
		if !strings.Contains(got, `prefix "claude"`) || !strings.Contains(got, "does not mean the issue is absent") {
			t.Errorf("hint = %q, want the foreign-prefix hint for claude-x1", got)
		}
	})
}

// lookupRecordingStore is showMissStubStore with every store read recorded,
// the looked-up id written as <ID> and its hash part as <HASH> so the reads of
// two misses compare.
type lookupRecordingStore struct {
	showMissStubStore
	id    string
	calls *[]string
}

func (s lookupRecordingStore) rec(call string, args ...string) {
	arg := strings.ReplaceAll(strings.Join(args, ","), s.id, "<ID>")
	if hash := s.id[strings.LastIndex(s.id, "-")+1:]; hash != "" {
		arg = strings.ReplaceAll(arg, hash, "<HASH>")
	}
	*s.calls = append(*s.calls, call+"("+arg+")")
}

func (s lookupRecordingStore) SearchIssues(ctx context.Context, q string, f types.IssueFilter) ([]*types.Issue, error) {
	s.rec("SearchIssues", append([]string{q}, f.IDs...)...)
	return s.showMissStubStore.SearchIssues(ctx, q, f)
}

func (s lookupRecordingStore) SearchIssueIDs(ctx context.Context, q string, f types.IssueFilter) ([]string, error) {
	s.rec("SearchIssueIDs", append([]string{q}, f.IDs...)...)
	return s.showMissStubStore.SearchIssueIDs(ctx, q, f)
}

func (s lookupRecordingStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	s.rec("GetIssue", id)
	return s.showMissStubStore.GetIssue(ctx, id)
}

func (s lookupRecordingStore) GetConfig(ctx context.Context, key string) (string, error) {
	s.rec("GetConfig", key)
	return s.showMissStubStore.GetConfig(ctx, key)
}

func (s lookupRecordingStore) GetAllConfig(ctx context.Context) (map[string]string, error) {
	s.rec("GetAllConfig")
	return s.showMissStubStore.GetAllConfig(ctx)
}

// TestShowDirectRoute_MissIdentityInvariant pins what the hint change must
// not move. A foreign-prefix miss (new hint) and an own-prefix miss (old
// hint) make the same store reads for the id, print the same "Issue <ID> not
// found" line and return the same exit error; only the Hint line differs.
// Choosing the hint reads config and nothing else, so it never looks the id
// up a second time or anywhere new, and for a bare hash it reads nothing.
func TestShowDirectRoute_MissIdentityInvariant(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })
	rootCtx, jsonOutput, proxiedServerMode = context.Background(), false, false
	beadsDir := foreignPrefixTown(t, "")
	dbPath = filepath.Join(beadsDir, "dolt")

	type miss struct {
		reads []string
		lines string
		code  int
	}
	run := func(id string) miss {
		var calls []string
		store = lookupRecordingStore{id: id, calls: &calls}
		var err error
		stderr := captureStderr(t, func() { err = showCmd.RunE(showCmd, []string{id}) })
		var exit *exitError
		if !errors.As(err, &exit) {
			t.Fatalf("bd show %s: err = %v, want the not-found *exitError", id, err)
		}
		m := miss{reads: calls, code: exit.Code}
		for _, line := range strings.SplitAfter(stderr, "\n") {
			if !strings.HasPrefix(line, "Hint: ") {
				m.lines += strings.ReplaceAll(line, id, "<ID>")
			}
		}
		return m
	}

	foreign, own := run("herald-x7k"), run("openclaw-zz9")
	if len(own.reads) == 0 {
		t.Fatal("the direct route made no recorded store read; the invariant would be vacuous")
	}
	if !reflect.DeepEqual(foreign.reads, own.reads) {
		t.Errorf("store reads differ:\nforeign %q\nown     %q", foreign.reads, own.reads)
	}
	if foreign.lines != own.lines || own.lines != "Issue <ID> not found\n" {
		t.Errorf("non-hint stderr: foreign %q, own %q, want both %q", foreign.lines, own.lines, "Issue <ID> not found\n")
	}
	if foreign.code != own.code || own.code != 1 {
		t.Errorf("exit code: foreign %d, own %d, want both 1", foreign.code, own.code)
	}

	for _, id := range []string{"herald-x7k", "openclaw-zz9"} {
		var calls []string
		showNotFoundHintIn(context.Background(), lookupRecordingStore{id: id, calls: &calls}, beadsDir, id, false)
		for _, c := range calls {
			if !strings.HasPrefix(c, "GetConfig(") {
				t.Errorf("choosing the hint for %s made store read %s, want config reads only", id, c)
			}
		}
		if len(calls) == 0 {
			t.Errorf("choosing the hint for %s read no config; the config-only check would be vacuous", id)
		}
	}

	// A bare hash has no prefix that could be foreign, so choosing its hint
	// reads nothing at all.
	var calls []string
	if got, want := showNotFoundHintIn(context.Background(), lookupRecordingStore{id: "gea4h", calls: &calls}, beadsDir, "gea4h", false), showNotFoundHint("gea4h"); got != want || len(calls) != 0 {
		t.Errorf("choosing the hint for bare hash gea4h = (%q, reads %q), want (%q, no read)", got, calls, want)
	}
}
