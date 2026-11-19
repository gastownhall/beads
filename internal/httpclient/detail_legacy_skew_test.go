package httpclient

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// bd-uschhs (#5850) AC-7: the unresolvable_dependency_edges member is NEW on
// the wire, so every server that predates it keeps answering the detail view
// the way it always did — a positive unresolvable_dependencies count and no
// edge member. The client must carry that answer through VERBATIM: the count
// is the count-only disclosure the legacy server meant to publish, the edges
// slice stays nil, and no code path may reconcile the skew by inventing
// record IDs from the count (the records carry only what the database
// stored, and this client has no combined read to ask — see the
// GetDependenciesWithUnresolvable refusal below).
//
// The contrast row pins the other side of the decode: when a current server
// DOES publish the member, it passes through untouched, so the nil-edge
// assertion above cannot pass vacuously through a decode that drops the
// member on every body.
func TestGetIssueLegacyServerSkewKeepsCountOnlyDisclosure(t *testing.T) {
	const anchorBody = `{
		"id": "bd-skew",
		"title": "Anchor beside a cross-repo edge",
		"status": "open",
		"revision": "2",
		"dependency_count": 1,
		"unresolvable_dependencies": 1
	}`
	// The same server, one lane newer: the stored record travels beside the
	// count, exactly as GetDependenciesWithUnresolvable answers it locally.
	const edgesBody = `{
		"id": "bd-skew",
		"title": "Anchor beside a cross-repo edge",
		"status": "open",
		"revision": "2",
		"dependency_count": 1,
		"unresolvable_dependencies": 1,
		"unresolvable_dependency_edges": [
			{
				"issue_id": "bd-skew",
				"depends_on_id": "liveop-kmf",
				"type": "blocks"
			}
		]
	}`

	for _, tc := range []struct {
		name       string
		body       string
		checkEdges func(*testing.T, *issueops.IssueDetails)
	}{
		{"pre_change_server_count_only", anchorBody, func(t *testing.T, d *issueops.IssueDetails) {
			if d.UnresolvableDependencies == nil || *d.UnresolvableDependencies != 1 {
				t.Fatalf("UnresolvableDependencies = %v, want the legacy server's count of 1 retained", d.UnresolvableDependencies)
			}
			if d.UnresolvableDependencyEdges != nil {
				t.Fatalf("UnresolvableDependencyEdges = %+v, want nil: the pre-change body has no member and none may be invented", d.UnresolvableDependencyEdges)
			}
			if len(d.Dependencies) != 0 {
				t.Fatalf("Dependencies = %+v, want empty: no stub row may stand in for the missing far end", d.Dependencies)
			}
		}},
		{"current_server_records_pass_through", edgesBody, func(t *testing.T, d *issueops.IssueDetails) {
			if d.UnresolvableDependencyEdges == nil || len(d.UnresolvableDependencyEdges) != 1 {
				t.Fatalf("UnresolvableDependencyEdges = %+v, want the one published record", d.UnresolvableDependencyEdges)
			}
			rec := d.UnresolvableDependencyEdges[0]
			if rec.DependsOnID != "liveop-kmf" || rec.Type != "blocks" || rec.IssueID != "bd-skew" {
				t.Fatalf("edge record = %+v, want the stored liveop-kmf blocks record", rec)
			}
			if d.UnresolvableDependencies == nil || *d.UnresolvableDependencies != 1 {
				t.Fatalf("UnresolvableDependencies = %v, want 1 beside the record", d.UnresolvableDependencies)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == wire.PathContext:
					ctx := v0Context("proj-skew")
					ctx.Capabilities = []string{"issues.get"}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(ctx)
				case strings.HasPrefix(r.URL.Path, wire.PathIssues+"/"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, tc.body)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)

			base, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			client, err := wire.New(base, nil, wire.Options{HTTPClient: srv.Client()})
			if err != nil {
				t.Fatal(err)
			}
			store := New(Target{BaseURL: base}, client, nil)
			reader, err := store.IssueReader()
			if err != nil {
				t.Fatal(err)
			}

			details, err := reader.Get(t.Context(), issueops.GetRequest{ID: "bd-skew"})
			if err != nil {
				t.Fatalf("Reader.Get: %v", err)
			}
			if details == nil {
				t.Fatal("Reader.Get answered (nil, nil)")
			}
			if details.ID != "bd-skew" {
				t.Fatalf("details.ID = %q, want bd-skew", details.ID)
			}
			if details.DependencyCount == nil || *details.DependencyCount != 1 {
				t.Fatalf("DependencyCount = %v, want 1: the stored edge stays counted on every wire vintage", details.DependencyCount)
			}
			tc.checkEdges(t, details)
		})
	}
}

// The exact combined read has no wire operation, and the client refuses it
// with the typed sentinel rather than answering a falsely-complete empty
// result: over the wire, a legacy server's count-only detail cannot be told
// from a verified-empty combined read, so an empty answer here would lie
// exactly the way the pre-fix readers did (GH#5850). TestUnsupportedContract
// (completeness_test.go) drives every name in legitimatelyUnsupported —
// GetDependenciesWithUnresolvable included, with this rationale recorded on
// its row — through conformance.RunUnsupportedContract, which asserts the
// storage.ErrUnsupported sentinel. This test states the same refusal from
// the reader-facing direction so the two halves cannot drift apart.
func TestGetDependenciesWithUnresolvableIsTypedUnsupported(t *testing.T) {
	store := &Store{}
	_, _, err := store.GetDependenciesWithUnresolvable(t.Context(), "bd-any")
	if err == nil {
		t.Fatal("GetDependenciesWithUnresolvable over the wire answered (nil, nil, nil): a false empty result")
	}
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) || unsup.Op != "GetDependenciesWithUnresolvable" {
		t.Fatalf("GetDependenciesWithUnresolvable error = %v, want the typed unsupported refusal", err)
	}
}
