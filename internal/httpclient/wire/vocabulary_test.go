// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/vocabulary_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"errors"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/spec"
)

// The drift gates. Everything above proves the client BEHAVES; these three prove
// its three tables still describe the surface it is a client of. They are pure —
// no server, no database, no build tag — which is what lets them run in the
// unconditional PR job rather than in a tier only some pushes reach.
//
// They are the only place this package touches internal/httpapi. Production code
// deliberately does not: importing the server would drag the storage engine into
// every process that only wants to talk to one.

func TestOperationIdsMatchTheServerRouteTable(t *testing.T) {
	// One row per operation, so a renamed operationId fails to compile here
	// before it fails on the wire at runtime.
	pairs := []struct{ mine, theirs string }{
		{OpHealth, httpapi.OpHealth},
		{OpGetContext, httpapi.OpGetContext},
		{OpListReadyWork, httpapi.OpListReadyWork},
		{OpCountReadyWork, httpapi.OpCountReadyWork},
		{OpGetStats, httpapi.OpGetStats},
		{OpListIssues, httpapi.OpListIssues},
		{OpQueryIssues, httpapi.OpQueryIssues},
		{OpGetIssue, httpapi.OpGetIssue},
		{OpClaimIssue, httpapi.OpClaimIssue},
		{OpCloseIssue, httpapi.OpCloseIssue},
		{OpReopenIssue, httpapi.OpReopenIssue},
		{OpUpdateIssue, httpapi.OpUpdateIssue},
		{OpSweepIssues, httpapi.OpSweepIssues},
		{OpDeleteIssues, httpapi.OpDeleteIssues},
		{OpBatchCreateIssues, httpapi.OpBatchCreateIssues},
		{OpBatchCloseIssues, httpapi.OpBatchCloseIssues},
		{OpClaimNextIssue, httpapi.OpClaimNextIssue},
		{OpReleaseIssue, httpapi.OpReleaseIssue},
		{OpCountIssues, httpapi.OpCountIssues},
		{OpListSettings, httpapi.OpListSettings},
		{OpGetSetting, httpapi.OpGetSetting},
		{OpSetSetting, httpapi.OpSetSetting},
		{OpUnsetSetting, httpapi.OpUnsetSetting},
		{OpListDependencies, httpapi.OpListDependencies},
		{OpListBlockingAnnotations, httpapi.OpListBlockingAnnotations},
		{OpGetDependencyTree, httpapi.OpGetDependencyTree},
		{OpCountDependencyEdges, httpapi.OpCountDependencyEdges},
		{OpListRelatedIssues, httpapi.OpListRelatedIssues},
		{OpAddComment, httpapi.OpAddComment},
		{OpListDependencyCycles, httpapi.OpListDependencyCycles},
		{OpAddDependencies, httpapi.OpAddDependencies},
		{OpRemoveDependency, httpapi.OpRemoveDependency},
		{OpListMemories, httpapi.OpListMemories},
		{OpRememberMemory, httpapi.OpRememberMemory},
		{OpGetMemory, httpapi.OpGetMemory},
		{OpForgetMemory, httpapi.OpForgetMemory},
		{OpCreateIssue, httpapi.OpCreateIssue},
		{OpApplyBatch, httpapi.OpApplyBatch},
		{OpCompareAndSetMetadata, httpapi.OpCompareAndSetMetadata},
		{OpListEvents, httpapi.OpListEvents},
		{OpWatchEvents, httpapi.OpWatchEvents},
		{OpBatchGetIssues, httpapi.OpBatchGetIssues},
	}

	// The table is a HAND LIST, and a hand list's failure mode is omission: a new
	// operation that never got a row here is an operation whose id this client
	// could spell differently from the server forever, and every row above would
	// still pass. Counting it against the capability map — which the route-table
	// gate below already holds set-equal with the server's own — is what makes
	// the omission fail instead of going unnoticed.
	//
	// The two identity operations are the difference: they carry no capability
	// token (liveness and the handshake are not gated by the list they publish),
	// so they are on this table and not in that map.
	if got, want := len(pairs), len(opCapability); got != want {
		t.Errorf("the identity table has %d rows against %d operations on the capability map; "+
			"a new operation needs a row here as well as a token there", got, want)
	}

	for _, tc := range pairs {
		if tc.mine != tc.theirs {
			t.Errorf("operation id %q, server says %q", tc.mine, tc.theirs)
		}
		if _, known := CapabilityFor(tc.mine); !known {
			t.Errorf("operation %q is not on the capability map", tc.mine)
		}
	}
}

func TestCapabilityTableMatchesTheServerRouteTable(t *testing.T) {
	// Set equality against the tokens the server actually advertises, so an
	// operation added upstream with a capability cannot arrive here unclassified —
	// the pre-flight would then dial it blind and get the bare 404 that is
	// ambiguous with an entity's not_found. The server's advertised set is the
	// UNION of its per-operation tokens and its server-wide BEHAVIOR tokens
	// (project.enforce), so the client's side of the comparison is the same union:
	// opCapability's tokens plus behaviorCapabilities. This is also the sync-
	// atomicity gate — if the server's project-enforce change landed without this
	// client's behaviorCapabilities mirror, the two sets diverge and this fails.
	var mine []string
	for op, token := range opCapability {
		if token == "" {
			// Liveness and the handshake itself are not gated by the list they
			// publish.
			if op != OpHealth && op != OpGetContext {
				t.Errorf("operation %q has no capability token", op)
			}
			continue
		}
		mine = append(mine, token)
	}
	mine = append(mine, behaviorCapabilities...)
	slices.Sort(mine)

	theirs := httpapi.Capabilities()
	if !slices.Equal(mine, theirs) {
		t.Errorf("capability tokens differ.\nclient: %v\nserver: %v", mine, theirs)
	}
}

// TestTheProjectIdentityVocabularyMatchesTheServer holds the three constants this
// client mirrors from internal/httpapi — the stamp header, the behavior
// capability and the mismatch reason — set-equal with the server's own, so a
// rename on either side fails to compile or fails here rather than silently on the
// wire. They are redeclared rather than imported for the reason paths.go states:
// internal/httpapi is the SERVER, and importing it would drag the storage engine
// into every client process.
func TestTheProjectIdentityVocabularyMatchesTheServer(t *testing.T) {
	if ProjectIDHeader != httpapi.ProjectIDHeader {
		t.Errorf("ProjectIDHeader = %q, server says %q", ProjectIDHeader, httpapi.ProjectIDHeader)
	}
	if CapProjectEnforce != httpapi.CapProjectEnforce {
		t.Errorf("CapProjectEnforce = %q, server says %q", CapProjectEnforce, httpapi.CapProjectEnforce)
	}
	if CapListSort != httpapi.CapIssuesListSort {
		t.Errorf("CapListSort = %q, server says %q", CapListSort, httpapi.CapIssuesListSort)
	}
	if CapCountScope != httpapi.CapIssuesCountScope {
		t.Errorf("CapCountScope = %q, server says %q", CapCountScope, httpapi.CapIssuesCountScope)
	}
	if CapBatchApplyLarge != httpapi.CapBatchApplyLarge {
		t.Errorf("CapBatchApplyLarge = %q, server says %q", CapBatchApplyLarge, httpapi.CapBatchApplyLarge)
	}
	if ReasonProjectMismatch != string(httpapi.ReasonProjectMismatch) {
		t.Errorf("ReasonProjectMismatch = %q, server says %q", ReasonProjectMismatch, httpapi.ReasonProjectMismatch)
	}
	if WireRevisionHeader != httpapi.WireRevisionHeader {
		t.Errorf("WireRevisionHeader = %q, server says %q", WireRevisionHeader, httpapi.WireRevisionHeader)
	}
	if ReasonWireRevisionUnsupported != string(httpapi.ReasonWireRevisionUnsupported) {
		t.Errorf("ReasonWireRevisionUnsupported = %q, server says %q", ReasonWireRevisionUnsupported, httpapi.ReasonWireRevisionUnsupported)
	}
	// ClientWireRevision is this build's OWN declared revision, not a mirror of
	// a server constant — but in this repo client and server ship from the same
	// commit, so the two are held in LOCKSTEP: ClientWireRevision must equal
	// httpapi.CurrentWireRevision exactly, not merely satisfy <=. A real
	// deployment can run an older client against a newer server (that is the
	// whole reason ClientMinWireRevision and the wire_revision_unsupported
	// refusal exist), but THIS package's own declared revision has no excuse to
	// lag the server it ships beside — a PR that bumps CurrentWireRevision
	// without also bumping ClientWireRevision has shipped a client that cannot
	// decode its own paired server's new shape, which is exactly the gap the
	// two checks below catch from both directions: strictly newer is one kind
	// of bug (a shape this build cannot possibly have been compiled to decode,
	// since CurrentWireRevision is the newest that exists), and merely
	// different-in-either-direction is the lockstep rule's own general case.
	if ClientWireRevision > httpapi.CurrentWireRevision {
		t.Errorf("ClientWireRevision = %d, which is newer than the server's own CurrentWireRevision %d", ClientWireRevision, httpapi.CurrentWireRevision)
	}
	if ClientWireRevision != httpapi.CurrentWireRevision {
		t.Errorf("ClientWireRevision = %d, server's CurrentWireRevision = %d; this client's declared revision has drifted from the one it was built against", ClientWireRevision, httpapi.CurrentWireRevision)
	}
	// The behavior mirror is exactly the server's, so the union above cannot pass
	// by coincidence — a behavior token on one side only would fail here. The
	// literal is spelled out member by member rather than compared against the
	// server's own slice: a mirror checked against the thing it mirrors passes
	// however both of them move.
	if !slices.Equal(behaviorCapabilities, []string{httpapi.CapProjectEnforce, httpapi.CapBatchApplyLarge, httpapi.CapIssuesListSort, httpapi.CapIssuesCountScope}) {
		t.Errorf("behaviorCapabilities = %v, want the server's project.enforce, issues.batchApplyLarge, issues.list.sort and issues.count.scope tokens", behaviorCapabilities)
	}
}

func TestEveryBaselineOperationIsOnTheCapabilityMap(t *testing.T) {
	// The two-speed policy is only sound if the baseline set is a subset of the
	// operations this client knows at all: a baseline op missing from the map
	// would pre-flight as an unknown operation instead of skipping.
	for op := range baselineOps {
		if _, known := CapabilityFor(op); !known {
			t.Errorf("baseline operation %q is not on the capability map", op)
		}
	}
	if len(baselineOps) != 5 {
		t.Errorf("baseline set has %d operations, want five (claimIssue is first-slice but gated as a write, ga-b8ddd.11)", len(baselineOps))
	}
}

func TestTheCodeTableIsSetEqualWithTheDocumentedVocabulary(t *testing.T) {
	documented := documentedCodes(t)
	mapped := Codes()

	if !slices.Equal(documented, mapped) {
		t.Errorf("problem-code vocabulary differs.\ndocument: %v\nclient:   %v", documented, mapped)
	}

	// The other half of the claim: being ON the table has to mean the code
	// reaches a row rather than the status-class default branch. 418 is frozen to
	// no code, so a code that fell through would answer with the 4xx default.
	for _, code := range mapped {
		got := sentinelFor(&ProblemError{Code: code, Status: 418}, target{})
		if errors.Is(got, ErrBadRequest) {
			t.Errorf("code %q is on the table but falls through to the default branch", code)
		}
	}
	// And the converse, so the probe above is meaningful.
	if got := sentinelFor(&ProblemError{Code: "no_such_code", Status: 418}, target{}); !errors.Is(got, ErrBadRequest) {
		t.Errorf("an unmapped code did not take the default branch: %v", got)
	}
}

func TestEveryDocumentedCodeCarriesTheStatusTheServerFroze(t *testing.T) {
	// The client dispatches on `code` alone, which is only safe because the
	// server freezes one status per code. If that ever stopped being true, the
	// default branch's status-class fallback would be reading a status that no
	// longer means anything.
	for _, code := range documentedCodes(t) {
		if httpapi.Code(code).Status() == 0 {
			t.Errorf("code %q has no frozen status on the server", code)
		}
	}
}

// documentedCodes collects every `x-bd-codes` entry in the shipped OpenAPI
// document — the per-operation vocabulary rows and the shared component
// responses alike — sorted and deduplicated.
func documentedCodes(t *testing.T) []string {
	t.Helper()
	var doc any
	if err := yaml.Unmarshal(spec.OpenAPIV0(), &doc); err != nil {
		t.Fatalf("parse the embedded openapi document: %v", err)
	}
	seen := map[string]struct{}{}
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			for key, value := range n {
				if key == "x-bd-codes" {
					list, ok := value.([]any)
					if !ok {
						t.Fatalf("x-bd-codes is %T, want a list", value)
					}
					for _, item := range list {
						code, ok := item.(string)
						if !ok {
							t.Fatalf("x-bd-codes entry is %T, want a string", item)
						}
						seen[code] = struct{}{}
					}
					continue
				}
				walk(value)
			}
		case []any:
			for _, item := range n {
				walk(item)
			}
		}
	}
	walk(doc)

	if len(seen) == 0 {
		t.Fatal("no x-bd-codes rows found; the document's shape changed and this gate is now vacuous")
	}
	out := make([]string, 0, len(seen))
	for code := range seen {
		out = append(out, code)
	}
	slices.Sort(out)
	return out
}
