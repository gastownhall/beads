package ado

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
)

func TestPullLinks_DirectRelations(t *testing.T) {
	wi := &WorkItem{
		ID: 100,
		Relations: []WorkItemRelation{
			{
				Rel: RelDependsOn,
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/200",
			},
			{
				Rel: RelChild,
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/300",
			},
			{
				Rel: RelRelated,
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/400",
			},
		},
	}

	resolver := NewLinkResolver(NewClient(NewSecretString("pat"), "org", "proj"))
	deps := resolver.PullLinks(wi)

	if len(deps) != 3 {
		t.Fatalf("got %d deps, want 3", len(deps))
	}

	// Sort by ToExternalID for deterministic assertions.
	sort.Slice(deps, func(i, j int) bool {
		return deps[i].ToExternalID < deps[j].ToExternalID
	})

	// Dependency-Forward: 100 blocks 200 (no swap)
	assertDep(t, deps[0], "100", "200", "blocks")
	// Hierarchy-Forward: 100 is parent of 300 (no swap)
	assertDep(t, deps[1], "100", "300", "parent")
	// Related: 100 related to 400
	assertDep(t, deps[2], "100", "400", "related")
}

func TestPullLinks_ReverseDirectionNormalization(t *testing.T) {
	wi := &WorkItem{
		ID: 100,
		Relations: []WorkItemRelation{
			{
				Rel: RelDependencyOf, // Dependency-Reverse: target blocks this → swap
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/200",
			},
			{
				Rel: RelParent, // Hierarchy-Reverse: target is parent of this → swap
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/300",
			},
		},
	}

	resolver := NewLinkResolver(NewClient(NewSecretString("pat"), "org", "proj"))
	deps := resolver.PullLinks(wi)

	if len(deps) != 2 {
		t.Fatalf("got %d deps, want 2", len(deps))
	}

	sort.Slice(deps, func(i, j int) bool {
		return deps[i].FromExternalID < deps[j].FromExternalID
	})

	// Dependency-Reverse swapped: 200 blocks 100
	assertDep(t, deps[0], "200", "100", "blocks")
	// Hierarchy-Reverse swapped: 300 is parent of 100
	assertDep(t, deps[1], "300", "100", "parent")
}

func TestPullLinks_DiscoveredFrom(t *testing.T) {
	wi := &WorkItem{
		ID: 100,
		Relations: []WorkItemRelation{
			{
				Rel: RelRelated,
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/500",
				Attributes: map[string]interface{}{
					"comment": "beads:discovered-from",
				},
			},
		},
	}

	resolver := NewLinkResolver(NewClient(NewSecretString("pat"), "org", "proj"))
	deps := resolver.PullLinks(wi)

	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1", len(deps))
	}
	assertDep(t, deps[0], "100", "500", "discovered-from")
}

func TestPullLinks_SkipNonLinks(t *testing.T) {
	wi := &WorkItem{
		ID: 100,
		Relations: []WorkItemRelation{
			{
				Rel: "AttachedFile",
				URL: "https://dev.azure.com/org/proj/_apis/wit/attachments/abc",
			},
			{
				Rel: "ArtifactLink",
				URL: "vstfs:///Build/Build/123",
			},
			{
				Rel: RelRelated,
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/200",
			},
		},
	}

	resolver := NewLinkResolver(NewClient(NewSecretString("pat"), "org", "proj"))
	deps := resolver.PullLinks(wi)

	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1 (non-links should be skipped)", len(deps))
	}
	assertDep(t, deps[0], "100", "200", "related")
}

func TestPullLinks_EmptyRelations(t *testing.T) {
	resolver := NewLinkResolver(NewClient(NewSecretString("pat"), "org", "proj"))

	// nil relations
	deps := resolver.PullLinks(&WorkItem{ID: 1})
	if deps != nil {
		t.Errorf("expected nil for nil relations, got %v", deps)
	}

	// empty slice
	deps = resolver.PullLinks(&WorkItem{ID: 1, Relations: []WorkItemRelation{}})
	if deps != nil {
		t.Errorf("expected nil for empty relations, got %v", deps)
	}
}

func TestAdoRelToBeadsDep(t *testing.T) {
	tests := []struct {
		name       string
		rel        string
		attributes map[string]interface{}
		wantType   string
		wantSwap   bool
	}{
		{
			name:     "DependsOn → blocks, no swap",
			rel:      RelDependsOn,
			wantType: "blocks",
			wantSwap: false,
		},
		{
			name:     "DependencyOf → blocks, swap",
			rel:      RelDependencyOf,
			wantType: "blocks",
			wantSwap: true,
		},
		{
			name:     "Child → parent, no swap",
			rel:      RelChild,
			wantType: "parent",
			wantSwap: false,
		},
		{
			name:     "Parent → parent, swap",
			rel:      RelParent,
			wantType: "parent",
			wantSwap: true,
		},
		{
			name:     "Related → related",
			rel:      RelRelated,
			wantType: "related",
			wantSwap: false,
		},
		{
			name: "Related with discovered-from comment → discovered-from",
			rel:  RelRelated,
			attributes: map[string]interface{}{
				"comment": "beads:discovered-from",
			},
			wantType: "discovered-from",
			wantSwap: false,
		},
		{
			name: "Related with other comment → related",
			rel:  RelRelated,
			attributes: map[string]interface{}{
				"comment": "some other comment",
			},
			wantType: "related",
			wantSwap: false,
		},
		{
			name:     "Unknown rel type → empty",
			rel:      "System.LinkTypes.Unknown",
			wantType: "",
			wantSwap: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			depType, swap := adoRelToBeadsDep(tt.rel, tt.attributes)
			if depType != tt.wantType {
				t.Errorf("depType = %q, want %q", depType, tt.wantType)
			}
			if swap != tt.wantSwap {
				t.Errorf("swap = %v, want %v", swap, tt.wantSwap)
			}
		})
	}
}

func TestBeadsDepToADORel(t *testing.T) {
	tests := []struct {
		depType string
		wantRel string
	}{
		{"blocks", RelDependsOn},
		{"parent", RelChild},
		{"related", RelRelated},
		{"discovered-from", RelRelated},
		{"unknown", RelRelated}, // default
	}

	for _, tt := range tests {
		t.Run(tt.depType, func(t *testing.T) {
			got := beadsDepToADORel(tt.depType)
			if got != tt.wantRel {
				t.Errorf("beadsDepToADORel(%q) = %q, want %q", tt.depType, got, tt.wantRel)
			}
		})
	}
}

func TestExtractWorkItemID(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantID  int
		wantErr bool
	}{
		{
			name:   "valid cloud URL",
			url:    "https://dev.azure.com/org/proj/_apis/wit/workitems/42",
			wantID: 42,
		},
		{
			name:   "valid on-prem URL",
			url:    "https://tfs.company.com/org/proj/_apis/wit/workitems/999",
			wantID: 999,
		},
		{
			name:   "large ID",
			url:    "https://dev.azure.com/org/proj/_apis/wit/workitems/123456",
			wantID: 123456,
		},
		{
			name:    "no numeric suffix",
			url:     "https://dev.azure.com/org/proj/_apis/wit/workitems",
			wantErr: true,
		},
		{
			name:    "empty URL",
			url:     "",
			wantErr: true,
		},
		{
			name:    "trailing slash",
			url:     "https://dev.azure.com/org/proj/_apis/wit/workitems/42/",
			wantErr: true,
		},
		{
			name:   "URL with query params",
			url:    "https://dev.azure.com/org/proj/_apis/wit/workitems/42?api-version=7.1",
			wantID: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := extractWorkItemID(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got id=%d", id)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("id = %d, want %d", id, tt.wantID)
			}
		})
	}
}

func TestIsLinkRelation(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{RelDependsOn, true},
		{RelDependencyOf, true},
		{RelChild, true},
		{RelParent, true},
		{RelRelated, true},
		{"AttachedFile", false},
		{"ArtifactLink", false},
		{"Hyperlink", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			got := isLinkRelation(tt.rel)
			if got != tt.want {
				t.Errorf("isLinkRelation(%q) = %v, want %v", tt.rel, got, tt.want)
			}
		})
	}
}

func TestPushLinks_AddMissing(t *testing.T) {
	var mu sync.Mutex
	var patchCalls []patchCall

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var ops []PatchOperation
		_ = json.Unmarshal(body, &ops)
		mu.Lock()
		patchCalls = append(patchCalls, patchCall{ops: ops})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)

	client, err := NewClient(NewSecretString("pat"), "org", "proj").
		WithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("WithBaseURL error: %v", err)
	}
	client = client.WithHTTPClient(ts.Client())
	resolver := NewLinkResolver(client)

	desired := []tracker.DependencyInfo{
		{FromExternalID: "100", ToExternalID: "200", Type: "blocks"},
		{FromExternalID: "100", ToExternalID: "300", Type: "parent"},
	}

	errs := resolver.PushLinks(context.Background(), 100, nil, desired)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(patchCalls) != 2 {
		t.Fatalf("got %d PATCH calls, want 2", len(patchCalls))
	}

	// Verify each call was an "add" to /relations/-
	for i, call := range patchCalls {
		if len(call.ops) != 1 {
			t.Errorf("call %d: got %d ops, want 1", i, len(call.ops))
			continue
		}
		if call.ops[0].Op != "add" {
			t.Errorf("call %d: op = %q, want %q", i, call.ops[0].Op, "add")
		}
		if call.ops[0].Path != "/relations/-" {
			t.Errorf("call %d: path = %q, want %q", i, call.ops[0].Path, "/relations/-")
		}
	}
}

func TestPushLinks_RemoveStale(t *testing.T) {
	var mu sync.Mutex
	var patchCalls []patchCall

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var ops []PatchOperation
		_ = json.Unmarshal(body, &ops)
		mu.Lock()
		patchCalls = append(patchCalls, patchCall{ops: ops})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)

	client, err := NewClient(NewSecretString("pat"), "org", "proj").
		WithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("WithBaseURL error: %v", err)
	}
	client = client.WithHTTPClient(ts.Client())
	resolver := NewLinkResolver(client)

	currentRelations := []WorkItemRelation{
		{
			Rel: RelDependsOn,
			URL: ts.URL + "/proj/_apis/wit/workitems/200",
		},
		{
			Rel: RelChild,
			URL: ts.URL + "/proj/_apis/wit/workitems/300",
		},
	}

	// No desired deps → both should be removed.
	errs := resolver.PushLinks(context.Background(), 100, currentRelations, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(patchCalls) != 2 {
		t.Fatalf("got %d PATCH calls, want 2", len(patchCalls))
	}

	// Verify removes happen in reverse index order (index 1 first, then 0).
	if patchCalls[0].ops[0].Path != "/relations/1" {
		t.Errorf("first remove path = %q, want /relations/1", patchCalls[0].ops[0].Path)
	}
	if patchCalls[1].ops[0].Path != "/relations/0" {
		t.Errorf("second remove path = %q, want /relations/0", patchCalls[1].ops[0].Path)
	}

	for i, call := range patchCalls {
		if call.ops[0].Op != "remove" {
			t.Errorf("call %d: op = %q, want %q", i, call.ops[0].Op, "remove")
		}
	}
}

func TestPushLinks_Idempotent(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)

	client, err := NewClient(NewSecretString("pat"), "org", "proj").
		WithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("WithBaseURL error: %v", err)
	}
	client = client.WithHTTPClient(ts.Client())
	resolver := NewLinkResolver(client)

	currentRelations := []WorkItemRelation{
		{
			Rel: RelDependsOn,
			URL: ts.URL + "/proj/_apis/wit/workitems/200",
		},
	}

	desired := []tracker.DependencyInfo{
		{FromExternalID: "100", ToExternalID: "200", Type: "blocks"},
	}

	errs := resolver.PushLinks(context.Background(), 100, currentRelations, desired)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if callCount != 0 {
		t.Errorf("expected 0 API calls for idempotent case, got %d", callCount)
	}
}

func TestPushLinks_PartialFailure(t *testing.T) {
	callNum := 0
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callNum++
		n := callNum
		mu.Unlock()

		if n == 1 {
			// First add fails.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"link error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)

	client, err := NewClient(NewSecretString("pat"), "org", "proj").
		WithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("WithBaseURL error: %v", err)
	}
	client = client.WithHTTPClient(ts.Client())
	resolver := NewLinkResolver(client)

	desired := []tracker.DependencyInfo{
		{FromExternalID: "100", ToExternalID: "200", Type: "blocks"},
		{FromExternalID: "100", ToExternalID: "300", Type: "related"},
	}

	errs := resolver.PushLinks(context.Background(), 100, nil, desired)

	// Should have exactly 1 error (first call failed, second succeeded).
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "link error") {
		t.Errorf("error should mention link error: %v", errs[0])
	}

	mu.Lock()
	defer mu.Unlock()
	// Both links should have been attempted.
	if callNum != 2 {
		t.Errorf("expected 2 API calls (continue on failure), got %d", callNum)
	}
}

func TestExtractWorkItemID_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"non-URL string", "not-a-url-at-all", true},
		{"alphabetic suffix", "https://dev.azure.com/org/proj/_apis/wit/workitems/abc", true},
		{"only slash", "/", true},
		{"numeric only", "/42", false},
		{"URL with query params after ID", "https://dev.azure.com/org/proj/_apis/wit/workitems/42?api-version=6.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := extractWorkItemID(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for URL %q, got id=%d", tt.url, id)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for URL %q: %v", tt.url, err)
			}
		})
	}
}

func TestHasDiscoveredFromAttribute(t *testing.T) {
	tests := []struct {
		name string
		attr map[string]interface{}
		want bool
	}{
		{"nil attributes", nil, false},
		{"empty attributes", map[string]interface{}{}, false},
		{"no comment key", map[string]interface{}{"name": "test"}, false},
		{"non-string comment", map[string]interface{}{"comment": 42}, false},
		{"empty comment string", map[string]interface{}{"comment": ""}, false},
		{"non-matching comment", map[string]interface{}{"comment": "some other link"}, false},
		{"matching comment", map[string]interface{}{"comment": "beads:discovered-from"}, true},
		{"matching comment with extra text", map[string]interface{}{"comment": "link beads:discovered-from here"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasDiscoveredFromAttribute(tt.attr)
			if got != tt.want {
				t.Errorf("hasDiscoveredFromAttribute(%v) = %v, want %v", tt.attr, got, tt.want)
			}
		})
	}
}

func TestPullLinks_BadURL(t *testing.T) {
	wi := &WorkItem{
		ID: 100,
		Relations: []WorkItemRelation{
			{
				Rel: RelDependsOn,
				URL: "not-a-valid-url",
			},
			{
				Rel: RelRelated,
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/200",
			},
		},
	}

	resolver := NewLinkResolver(NewClient(NewSecretString("pat"), "org", "proj"))
	deps := resolver.PullLinks(wi)

	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1 (bad URL should be skipped)", len(deps))
	}
	assertDep(t, deps[0], "100", "200", "related")
}

func TestPullLinks_UnknownRelType(t *testing.T) {
	wi := &WorkItem{
		ID: 100,
		Relations: []WorkItemRelation{
			{
				Rel: "System.LinkTypes.SomethingNew",
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/200",
			},
			{
				Rel: RelRelated,
				URL: "https://dev.azure.com/org/proj/_apis/wit/workitems/300",
			},
		},
	}

	resolver := NewLinkResolver(NewClient(NewSecretString("pat"), "org", "proj"))
	deps := resolver.PullLinks(wi)

	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1 (unknown rel type should be skipped)", len(deps))
	}
	assertDep(t, deps[0], "100", "300", "related")
}

func TestPullLinks_NilAttributes(t *testing.T) {
	wi := &WorkItem{
		ID: 100,
		Relations: []WorkItemRelation{
			{
				Rel:        RelRelated,
				URL:        "https://dev.azure.com/org/proj/_apis/wit/workitems/200",
				Attributes: nil,
			},
		},
	}

	resolver := NewLinkResolver(NewClient(NewSecretString("pat"), "org", "proj"))
	deps := resolver.PullLinks(wi)

	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1", len(deps))
	}
	assertDep(t, deps[0], "100", "200", "related")
}

func TestPushLinks_InvalidExternalID(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)

	client, err := NewClient(NewSecretString("pat"), "org", "proj").
		WithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("WithBaseURL error: %v", err)
	}
	client = client.WithHTTPClient(ts.Client())
	resolver := NewLinkResolver(client)

	desired := []tracker.DependencyInfo{
		{FromExternalID: "100", ToExternalID: "not-a-number", Type: "blocks"},
		{FromExternalID: "100", ToExternalID: "200", Type: "related"},
	}

	errs := resolver.PushLinks(context.Background(), 100, nil, desired)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Only the valid dep (200) should have been pushed.
	if callCount != 1 {
		t.Errorf("expected 1 API call (invalid ID skipped), got %d", callCount)
	}
}

func TestPushLinks_RemoveFailure(t *testing.T) {
	callNum := 0
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callNum++
		n := callNum
		mu.Unlock()

		if n == 1 {
			// First remove fails (400 = permanent, no retry).
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"remove error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)

	client, err := NewClient(NewSecretString("pat"), "org", "proj").
		WithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("WithBaseURL error: %v", err)
	}
	client = client.WithHTTPClient(ts.Client())
	resolver := NewLinkResolver(client)

	currentRelations := []WorkItemRelation{
		{
			Rel: RelDependsOn,
			URL: ts.URL + "/proj/_apis/wit/workitems/200",
		},
		{
			Rel: RelChild,
			URL: ts.URL + "/proj/_apis/wit/workitems/300",
		},
	}

	// No desired deps → both should be removed, but first fails.
	errs := resolver.PushLinks(context.Background(), 100, currentRelations, nil)

	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "remove relation") {
		t.Errorf("error should mention remove relation: %v", errs[0])
	}

	mu.Lock()
	defer mu.Unlock()
	// Both removes should have been attempted despite first failing.
	if callNum != 2 {
		t.Errorf("expected 2 API calls (continue on failure), got %d", callNum)
	}
}

// patchCall records a PATCH request's operations for test assertions.
type patchCall struct {
	ops []PatchOperation
}

// assertDep is a test helper that asserts a DependencyInfo matches expected values.
func assertDep(t *testing.T, got tracker.DependencyInfo, wantFrom, wantTo, wantType string) {
	t.Helper()
	if got.FromExternalID != wantFrom {
		t.Errorf("FromExternalID = %q, want %q", got.FromExternalID, wantFrom)
	}
	if got.ToExternalID != wantTo {
		t.Errorf("ToExternalID = %q, want %q", got.ToExternalID, wantTo)
	}
	if got.Type != wantType {
		t.Errorf("Type = %q, want %q", got.Type, wantType)
	}
}
