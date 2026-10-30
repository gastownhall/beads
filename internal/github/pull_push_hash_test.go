package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/tracker/conformance"
	"github.com/steveyegge/beads/internal/types"
)

// oneIssueServer is a GitHub REST double for the calls a sync makes on a
// repository with one issue: the issue list, a GET of the issue, and a PATCH
// that replaces its labels, as GitHub does.
type oneIssueServer struct {
	mu         sync.Mutex
	issue      Issue
	patched    [][]string
	unexpected []string
}

func (s *oneIssueServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body interface{}
	switch r.Method + " " + r.URL.Path {
	case "GET /repos/o/r/issues":
		body = []Issue{s.issue}
	case "GET /repos/o/r/issues/1":
		body = s.issue
	case "PATCH /repos/o/r/issues/1":
		var fields struct {
			Labels []string `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.patched = append(s.patched, fields.Labels)
		s.issue.Labels = labelsNamed(fields.Labels...)
		body = s.issue
	default:
		s.unexpected = append(s.unexpected, r.Method+" "+r.URL.Path)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *oneIssueServer) editLabels(updatedAt time.Time, names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issue.Labels = labelsNamed(names...)
	s.issue.UpdatedAt = &updatedAt
}

func labelsNamed(names ...string) []Label {
	labels := make([]Label, 0, len(names))
	for _, name := range names {
		labels = append(labels, Label{Name: name})
	}
	return labels
}

// syncPushHooks mirrors the push hooks cmd/bd's buildGitHubPushHooks gives
// the engine of `bd github sync`, `push` and `pull`.
func syncPushHooks(gt *Tracker) *tracker.PushHooks {
	config := gt.MappingConfig()
	return &tracker.PushHooks{
		ContentEqual: func(local *types.Issue, remote *tracker.TrackerIssue) bool {
			gh, ok := remote.Raw.(*Issue)
			return ok && PushFieldsEqual(local, gh, config)
		},
		ContentHash: func(local *types.Issue) string { return PushContentHash(local, config) },
		TargetScope: gt.PushTargetScope,
	}
}

// TestPullMirrorsRemovedLabelsSoThePushIsANoOp drives the sync engine with
// GitHub's mapping and push hash against a REST double. GitHub drops labels
// the local issue still has, or changes labels the pull maps to the fields the
// local issue already has. The pull must leave the issue so that the next
// push of it makes no PATCH: one would put the removed labels back, or replace
// a priority label bd cannot map.
func TestPullMirrorsRemovedLabelsSoThePushIsANoOp(t *testing.T) {
	for _, test := range []struct {
		name         string
		plainLabels  []string // on both sides at the first push
		remoteLabels []string // on GitHub after its edit
		pullOnly     bool     // `bd github pull <id>`, else `bd github sync`
	}{
		{name: "sync/last_plain_label_removed", plainLabels: []string{"needs-triage"}, remoteLabels: []string{"type::task", "priority::medium"}},
		{name: "sync/only_an_unmapped_priority_left", plainLabels: []string{"needs-triage"}, remoteLabels: []string{"priority::urgent"}},
		{name: "pull/last_plain_label_removed", plainLabels: []string{"needs-triage"}, remoteLabels: []string{"type::task", "priority::medium"}, pullOnly: true},
		{name: "pull/only_an_unmapped_priority_left", plainLabels: []string{"needs-triage"}, remoteLabels: []string{"priority::urgent"}, pullOnly: true},
		// The pull finds these equal to the local issue and writes nothing.
		{name: "pull/equal_with_an_unmapped_priority", remoteLabels: []string{"type::task", "priority::urgent"}, pullOnly: true},
		{name: "pull/equal_with_every_label_removed", pullOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			ref := "https://github.com/o/r/issues/1"
			pushed := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
			server := &oneIssueServer{issue: Issue{
				ID: 1001, Number: 1, Title: "Fix it", Body: "Details", State: "open", HTMLURL: ref, UpdatedAt: &pushed,
				Labels: labelsNamed(append([]string{"type::task", "priority::medium"}, test.plainLabels...)...),
			}}
			srv := httptest.NewServer(server)
			defer srv.Close()

			gt := &Tracker{client: NewClient("token", "o", "r").WithBaseURL(srv.URL), config: DefaultMappingConfig()}
			store := conformance.NewStore()
			store.Config["issue_prefix"] = "bd"
			store.Issues["bd-1"] = &types.Issue{
				ID: "bd-1", Title: "Fix it", Description: "Details", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask, Labels: test.plainLabels,
				ExternalRef: &ref, UpdatedAt: pushed,
			}
			engine := tracker.NewEngine(gt, store, "test")
			engine.PushHooks = syncPushHooks(gt)
			run := func(opts tracker.SyncOptions) {
				t.Helper()
				if _, err := engine.Sync(ctx, opts); err != nil {
					t.Fatalf("Sync(%+v): %v", opts, err)
				}
			}

			// The issue matches GitHub, so this push only records its hash.
			run(tracker.SyncOptions{Push: true})

			server.editLabels(pushed.Add(time.Hour), test.remoteLabels...)
			if test.pullOnly {
				run(tracker.SyncOptions{Pull: true, IssueIDs: []string{"bd-1"}})
			} else {
				run(tracker.SyncOptions{Pull: true, Push: true})
			}
			if got := store.Snapshot().Issues["bd-1"].Labels; len(got) != 0 {
				t.Fatalf("local labels after the pull = %q, want none", got)
			}

			run(tracker.SyncOptions{Push: true})
			server.mu.Lock()
			defer server.mu.Unlock()
			if len(server.patched) != 0 || len(server.unexpected) != 0 {
				t.Fatalf("GitHub got PATCHes setting labels %q and unexpected requests %q, want neither",
					server.patched, server.unexpected)
			}
		})
	}
}
