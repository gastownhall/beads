package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// showFixtureStore is showMissStubStore (issue prefix "openclaw", no routes)
// holding the issues in found. An id in nilIssue resolves but reads back as
// no issue, the defensive nil-issue path; an id in failing resolves and then
// fails to read with a backend error. Everything else misses.
type showFixtureStore struct {
	showMissStubStore
	found    map[string]*types.Issue
	nilIssue map[string]bool
	failing  map[string]bool
}

func newShowFixtureStore(found ...string) showFixtureStore {
	s := showFixtureStore{
		found:    map[string]*types.Issue{},
		nilIssue: map[string]bool{"herald-n1l": true, "openclaw-n1l": true},
		failing:  map[string]bool{"herald-err1": true},
	}
	for _, id := range found {
		s.found[id] = &types.Issue{ID: id, Title: "Fixture " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	return s
}

func (s showFixtureStore) SearchIssues(_ context.Context, _ string, f types.IssueFilter) ([]*types.Issue, error) {
	for _, id := range f.IDs {
		if issue, ok := s.found[id]; ok {
			return []*types.Issue{issue}, nil
		}
		if s.nilIssue[id] || s.failing[id] {
			return []*types.Issue{{ID: id}}, nil
		}
	}
	return nil, nil
}

func (s showFixtureStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	switch {
	case s.found[id] != nil:
		return s.found[id], nil
	case s.nilIssue[id]:
		return nil, nil
	case s.failing[id]:
		return nil, errors.New("backend down")
	}
	return s.showMissStubStore.GetIssue(context.Background(), id)
}

func (showFixtureStore) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}

func (s showFixtureStore) IssueReader() (issueops.Reader, error) {
	return showFixtureReader{s: s}, nil
}

// showFixtureReader answers bd show --json's detail read from the fixture.
type showFixtureReader struct {
	issueops.Reader
	s showFixtureStore
}

func (r showFixtureReader) Get(_ context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	issue, ok := r.s.found[req.ID]
	if !ok {
		return nil, issueops.ErrNotFound
	}
	return &issueops.IssueDetails{Issue: *issue}, nil
}

// useShowFixture points bd show at s in a workspace with no routes.jsonl and
// no auto-route, the GH#5259 shape, in text or JSON mode.
func useShowFixture(t *testing.T, s showFixtureStore, asJSON bool) {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })
	store, rootCtx, jsonOutput, proxiedServerMode = s, context.Background(), asJSON, false
	dbPath = filepath.Join(foreignPrefixTown(t, ""), "dolt")
}

// runShowMode runs bd show in mode ("direct", "refs" or "children") for ids
// and returns its stdout and stderr apart, with its error.
func runShowMode(t *testing.T, mode string, ids ...string) (stdout, stderr string, err error) {
	t.Helper()
	if mode != "direct" {
		flag := showCmd.Flags().Lookup(mode)
		if err := flag.Value.Set("true"); err != nil {
			t.Fatalf("set --%s: %v", mode, err)
		}
		defer func() {
			_ = flag.Value.Set("false")
			flag.Changed = false
		}()
	}

	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("pipe: %v", perr)
	}
	errR, errW, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("pipe: %v", perr)
	}
	outDone, errDone := make(chan string, 1), make(chan string, 1)
	drain := func(r *os.File, done chan<- string) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}
	go drain(outR, outDone)
	go drain(errR, errDone)
	os.Stdout, os.Stderr = outW, errW
	func() {
		defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
		err = showCmd.RunE(showCmd, ids)
	}()
	_ = outW.Close()
	_ = errW.Close()
	stdout, stderr = <-outDone, <-errDone
	_ = outR.Close()
	_ = errR.Close()
	return stdout, stderr, err
}

// withoutHintLines drops stderr's "Hint: " lines and writes id as <ID>.
func withoutHintLines(stderr, id string) string {
	var kept string
	for _, line := range strings.SplitAfter(stderr, "\n") {
		if !strings.HasPrefix(line, "Hint: ") {
			kept += strings.ReplaceAll(line, id, "<ID>")
		}
	}
	return kept
}

// TestShowRefsChildren_ForeignPrefixMissHint pins AC-5 (GH#5259): bd show
// --refs and --children keep their current leading line and exit status for
// an unrouted foreign-prefix miss and add the direct route's limited-search
// hint after it, on the resolver's not-found and on the defensive nil-issue
// path alike. An own-prefix miss and a non-not-found error print exactly
// what they printed before, with no hint.
func TestShowRefsChildren_ForeignPrefixMissHint(t *testing.T) {
	for _, mode := range []string{"refs", "children"} {
		t.Run(mode, func(t *testing.T) {
			useShowFixture(t, newShowFixtureStore("openclaw-ab1"), false)

			for _, tc := range []struct {
				id        string
				wantLines string // stderr without Hint lines, id written <ID>
				wantHint  bool
			}{
				{id: "herald-x7k", wantLines: "Error resolving <ID>: no issue found matching \"<ID>\"\n", wantHint: true},
				{id: "openclaw-zz9", wantLines: "Error resolving <ID>: no issue found matching \"<ID>\"\n"},
				{id: "herald-n1l", wantLines: "Issue <ID> not found\n", wantHint: true},
				{id: "openclaw-n1l", wantLines: "Issue <ID> not found\n"},
				{id: "herald-err1", wantLines: "Error resolving <ID>: backend down\n"},
			} {
				_, stderr, err := runShowMode(t, mode, tc.id)
				if err != nil {
					t.Errorf("bd show --%s %s: err = %v, want the current success exit", mode, tc.id, err)
				}
				if got := withoutHintLines(stderr, tc.id); got != tc.wantLines {
					t.Errorf("bd show --%s %s: non-hint stderr = %q, want %q", mode, tc.id, got, tc.wantLines)
				}
				hint := strings.Contains(stderr, "Hint: ")
				if hint != tc.wantHint {
					t.Errorf("bd show --%s %s: stderr = %q, want a hint: %v", mode, tc.id, stderr, tc.wantHint)
				}
				if tc.wantHint && (!strings.Contains(stderr, `prefix "herald"`) || !strings.Contains(stderr, foreignPrefixHintMarker)) {
					t.Errorf("bd show --%s %s: stderr = %q, want the foreign-prefix hint", mode, tc.id, stderr)
				}
			}

			// A found id beside a foreign miss still renders, and the batch
			// still exits as it did.
			stdout, stderr, err := runShowMode(t, mode, "openclaw-ab1", "herald-x7k")
			if err != nil || !strings.Contains(stdout, "openclaw-ab1") || !strings.Contains(stderr, foreignPrefixHintMarker) {
				t.Errorf("bd show --%s openclaw-ab1 herald-x7k = (%v, stdout %q, stderr %q), want the found id rendered and the miss hinted", mode, err, stdout, stderr)
			}
		})
	}
}

// foreignPrefixHintMarker is a phrase only the foreign-prefix miss hint
// (foreignPrefixMissHint) prints; existingHintMarker one only
// showNotFoundHint prints.
const (
	foreignPrefixHintMarker = "does not mean the issue is absent"
	existingHintMarker      = "never existed"
)
