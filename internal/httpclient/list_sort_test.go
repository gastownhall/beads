// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/list_sort_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"slices"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/httpapi/spec"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// TestFlaglessListSortIsTheSameOrderNamed is the drift pin for the pushdown's
// one translation: issueops.ListRequest's empty SortBy goes onto the wire as
// `priority`, and that is sound only while the two are the SAME order.
//
// A served run cannot show this. It would answer correctly whichever spelling
// were sent, because the reference store on the far side is the same code
// making the same substitution — so the case that catches a wrong constant is
// this one, asked of the three places the equivalence has to hold at once.
//
// It sits in the default build rather than behind the cgo tag for the same
// reason the other two drift pins do: nothing here needs a database.
func TestFlaglessListSortIsTheSameOrderNamed(t *testing.T) {
	// THE DOCUMENT. The translation exists because an empty value is not in
	// the published enum — a server would refuse `sort=` — so what the client
	// substitutes has to be a member, and the absence of an empty member is
	// what keeps the substitution from being dead code.
	published := publishedParamEnum(t, wire.OpListIssues, "sort")
	if len(published) == 0 {
		t.Fatalf("the document publishes no `sort` enum on %s; this direction would assert nothing", wire.OpListIssues)
	}
	if !slices.Contains(published, flaglessListSort) {
		t.Errorf("flaglessListSort = %q, which %s does not publish (%v): the pushdown would earn a 400 on every "+
			"request nobody named a sort for", flaglessListSort, wire.OpListIssues, published)
	}
	if slices.Contains(published, "") {
		t.Errorf("%s now publishes an empty `sort` member (%v): ListRequest's own empty SortBy could be sent "+
			"verbatim and this translation is dead code rather than a necessity", wire.OpListIssues, published)
	}

	// THE SERVER'S ORDER BY, which is what actually decides which rows survive
	// the limit once the order is pushed down. Both directions, because the
	// pushdown emits `reverse` alongside `sort` and a spelling that agreed
	// ascending and disagreed descending would still lose rows.
	column := func(key string) string { return key }
	for _, reverse := range []bool{false, true} {
		flagless := sqlbuild.OrderByForColumns("", reverse, column)
		named := sqlbuild.OrderByForColumns(flaglessListSort, reverse, column)
		if flagless == "" {
			t.Fatalf("the flagless order renders no ORDER BY (reverse %v); it is a Go-side sort now, and the "+
				"pushdown cannot ask a server for an order the server does not express", reverse)
		}
		if flagless != named {
			t.Errorf("reverse %v: the flagless order renders %q and %q renders %q; the pushdown would ask the "+
				"server for a different order than a local `bd list` runs", reverse, flagless, flaglessListSort, named)
		}
	}

	// THE CLIENT'S OWN EPILOGUE, which is the half the pushdown newly leans on.
	// sortListRows still runs over the page the server ordered, and it runs
	// under the caller's spelling — the empty one — while the server ordered
	// under the substituted one. If those two produced different orders, the
	// fast leg would fetch the right rows and then shuffle them.
	//
	// The corpus is SCRAMBLED before each run so this compares two orders
	// rather than two no-ops.
	for _, reverse := range []bool{false, true} {
		flagless := flaglessSortCorpus()
		named := flaglessSortCorpus()
		sortListRows(flagless, "", reverse)
		sortListRows(named, flaglessListSort, reverse)
		if got, want := rowIDs(flagless), rowIDs(named); !slices.Equal(got, want) {
			t.Errorf("reverse %v: the client orders the flagless spelling %v and %q %v; the pushdown asks the "+
				"server for one and re-sorts the answer under the other", reverse, got, flaglessListSort, want)
		}
	}
}

func rowIDs(rows []*types.IssueWithCounts) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// flaglessSortCorpus is a fixed set of rows, in an order that is none of the
// ones under test, reaching every tie-break the flagless order carries: equal
// priorities decided by created time, equal priorities and equal instants
// decided by id, and ids whose natural and lexical orders disagree.
func flaglessSortCorpus() []*types.IssueWithCounts {
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	var corpus []*types.IssueWithCounts
	for _, seed := range []struct {
		id       string
		priority int
		minute   int
	}{
		{"bd-6", 2, 0}, {"bd-2", 0, 0}, {"bd-7", 3, 5}, {"bd-10", 0, 0},
		{"bd-4", 1, 0}, {"bd-1", 0, 0}, {"bd-5", 1, 5}, {"bd-3", 0, 5},
	} {
		at := base.Add(time.Duration(seed.minute) * time.Minute)
		corpus = append(corpus, &types.IssueWithCounts{Issue: &types.Issue{
			ID: seed.id, Title: seed.id, Status: types.StatusOpen, Priority: seed.priority,
			IssueType: types.TypeTask, CreatedAt: at, UpdatedAt: at,
		}})
	}
	return corpus
}

// publishedParamEnum reads one operation parameter's enum out of the wire
// contract itself, rather than out of a second copy of the names.
func publishedParamEnum(t *testing.T, operationID, param string) []string {
	t.Helper()
	// S3 reconciliation (2026-10): spec.OpenAPIV0() rather
	// than a relative os.ReadFile — the document is go:embed'd into
	// internal/httpapi/spec precisely so a reader does not depend on a
	// filesystem layout, and a relative path breaks the moment this runs
	// under Bazel's test sandbox, which copies in only what a target
	// declares as `data`. internal/httpclient/encode's own bijection gate
	// already reads the document this way for the same reason.
	raw := spec.OpenAPIV0()
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
			Parameters  []struct {
				Name   string `yaml:"name"`
				Schema struct {
					Enum []string `yaml:"enum"`
				} `yaml:"schema"`
			} `yaml:"parameters"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse the wire contract: %v", err)
	}
	for _, methods := range doc.Paths {
		for _, operation := range methods {
			if operation.OperationID != operationID {
				continue
			}
			for _, p := range operation.Parameters {
				if p.Name == param {
					return p.Schema.Enum
				}
			}
		}
	}
	t.Fatalf("the document publishes no %s parameter on %s", param, operationID)
	return nil
}
