package github

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPushFieldsEqual is the regression test for gastownhall/beads#4214:
// without a content comparator, GitHub push re-PATCHed every issue on every
// run. PushFieldsEqual must report "no change" when the pushable fields
// (title, body, state, label set) already match GitHub, and "changed"
// otherwise, so the engine can skip redundant updates.
func TestPushFieldsEqual(t *testing.T) {
	config := DefaultMappingConfig()

	ghLabels := func(names ...string) []Label {
		ls := make([]Label, 0, len(names))
		for _, n := range names {
			ls = append(ls, Label{Name: n})
		}
		return ls
	}

	base := &types.Issue{
		Title:       "Fix the thing",
		Description: "Some body text",
		IssueType:   types.IssueType("task"),
		Priority:    2, // -> priority::medium
		Status:      types.StatusOpen,
	}

	tests := []struct {
		name   string
		local  *types.Issue
		remote *Issue
		want   bool
	}{
		{
			name:  "identical, remote labels reordered",
			local: base,
			// GitHub does not preserve label order across a round-trip.
			remote: &Issue{Title: "Fix the thing", Body: "Some body text", State: "open",
				Labels: ghLabels("priority::medium", "type::task")},
			want: true,
		},
		{name: "nil local", local: nil, remote: &Issue{}, want: false},
		{name: "nil remote", local: base, remote: nil, want: false},
		{
			name:  "title differs",
			local: base,
			remote: &Issue{Title: "Different", Body: "Some body text", State: "open",
				Labels: ghLabels("type::task", "priority::medium")},
			want: false,
		},
		{
			name:  "body differs",
			local: base,
			remote: &Issue{Title: "Fix the thing", Body: "Changed", State: "open",
				Labels: ghLabels("type::task", "priority::medium")},
			want: false,
		},
		{
			name:  "state differs",
			local: base,
			remote: &Issue{Title: "Fix the thing", Body: "Some body text", State: "closed",
				Labels: ghLabels("type::task", "priority::medium")},
			want: false,
		},
		{
			name:  "extra label on remote",
			local: base,
			remote: &Issue{Title: "Fix the thing", Body: "Some body text", State: "open",
				Labels: ghLabels("type::task", "priority::medium", "extra")},
			want: false,
		},
		{
			name:  "priority label differs",
			local: base,
			remote: &Issue{Title: "Fix the thing", Body: "Some body text", State: "open",
				Labels: ghLabels("type::task", "priority::high")},
			want: false,
		},
		{
			name: "in_progress adds status label",
			local: &types.Issue{Title: "T", Description: "B", IssueType: "task",
				Priority: 2, Status: types.StatusInProgress},
			remote: &Issue{Title: "T", Body: "B", State: "open",
				Labels: ghLabels("type::task", "priority::medium", "status::in_progress")},
			want: true,
		},
		{
			name: "closed maps to state closed",
			local: &types.Issue{Title: "T", Description: "B", IssueType: "task",
				Priority: 2, Status: types.StatusClosed},
			remote: &Issue{Title: "T", Body: "B", State: "closed",
				Labels: ghLabels("type::task", "priority::medium")},
			want: true,
		},
		{
			name: "non-scoped local labels preserved in comparison",
			local: &types.Issue{Title: "T", Description: "B", IssueType: "task",
				Priority: 2, Status: types.StatusOpen, Labels: []string{"backend"}},
			remote: &Issue{Title: "T", Body: "B", State: "open",
				Labels: ghLabels("type::task", "priority::medium", "backend")},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PushFieldsEqual(tt.local, tt.remote, config); got != tt.want {
				t.Errorf("PushFieldsEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPushContentHash covers the local content fingerprint used to skip the
// per-issue GitHub fetch on a no-op push (gastownhall/beads#4214). It must be
// stable for identical content (and label reordering), and must change whenever
// any pushable field changes, so the engine never skips a needed update.
func TestPushContentHash(t *testing.T) {
	config := DefaultMappingConfig()

	base := &types.Issue{
		Title:       "Fix the thing",
		Description: "Some body text",
		IssueType:   types.IssueType("task"),
		Priority:    2,
		Status:      types.StatusOpen,
		Labels:      []string{"backend", "ops"},
	}

	h := func(i *types.Issue) string { return PushContentHash(i, config) }

	if h(base) == "" {
		t.Fatal("PushContentHash returned empty for a valid issue")
	}
	if PushContentHash(nil, config) != "" {
		t.Error("PushContentHash(nil) should return empty string")
	}

	// Stable across repeated calls.
	if h(base) != h(base) {
		t.Error("PushContentHash is not deterministic for identical input")
	}

	// Stable across non-scoped label reordering (GitHub does not preserve order).
	reordered := *base
	reordered.Labels = []string{"ops", "backend"}
	if h(base) != h(&reordered) {
		t.Error("PushContentHash changed when only label order changed")
	}

	// Every pushable field must perturb the hash.
	mutate := map[string]func(*types.Issue){
		"title":    func(i *types.Issue) { i.Title = "Different" },
		"body":     func(i *types.Issue) { i.Description = "Changed" },
		"status":   func(i *types.Issue) { i.Status = types.StatusClosed },
		"priority": func(i *types.Issue) { i.Priority = 1 },
		"type":     func(i *types.Issue) { i.IssueType = types.IssueType("bug") },
		"labels":   func(i *types.Issue) { i.Labels = []string{"backend"} },
	}
	for name, fn := range mutate {
		t.Run(name+" changes hash", func(t *testing.T) {
			mutated := *base
			mutated.Labels = append([]string(nil), base.Labels...)
			fn(&mutated)
			if h(&mutated) == h(base) {
				t.Errorf("PushContentHash unchanged after %s mutation", name)
			}
		})
	}
}

// TestPushContentHashInvalidatesPreFixHashes covers the repair half of
// gastownhall/beads#5486: builds before the fix cached the desired closed
// state's hash at create time even though the issue landed OPEN on GitHub, so
// every later push short-circuited as "already in sync". The hash is versioned
// so those stale stored hashes miss once; the sync that follows fetches the
// issue, sees the state mismatch, and closes it.
func TestPushContentHashInvalidatesPreFixHashes(t *testing.T) {
	config := DefaultMappingConfig()

	issue := &types.Issue{
		Title:       "Done already",
		Description: "Pushed closed, created OPEN pre-fix",
		IssueType:   types.TypeTask,
		Priority:    2,
		Status:      types.StatusClosed,
		Labels:      []string{"backend"},
	}

	// The pre-fix formula: sha256 over title, body, desired state and
	// NUL-joined sorted labels, each field followed by a 0 byte.
	preFixHash := func(i *types.Issue) string {
		desiredState := "open"
		if i.Status == types.StatusClosed {
			desiredState = "closed"
		}
		desiredLabels, _ := BeadsIssueToGitHubFields(i, config)["labels"].([]string)
		sortedLabels := append([]string(nil), desiredLabels...)
		slices.Sort(sortedLabels)
		h := sha256.New()
		for _, s := range []string{i.Title, i.Description, desiredState, strings.Join(sortedLabels, "\x00")} {
			_, _ = h.Write([]byte(s))
			_, _ = h.Write([]byte{0})
		}
		return hex.EncodeToString(h.Sum(nil))
	}

	if got, old := PushContentHash(issue, config), preFixHash(issue); got == old {
		t.Errorf("PushContentHash = pre-fix hash (%s), want a versioned hash that differs so already-stuck issues are re-fetched and closed", got)
	}
}
